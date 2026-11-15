package rqlite

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func copyDataResult(result *contract.DataEditResult) *contract.DataEditResult {
	if result == nil {
		return nil
	}
	copy := *result
	copy.Outcomes = append([]contract.DataEditOutcome(nil), result.Outcomes...)
	for i, outcome := range result.Outcomes {
		original := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: outcome.Original}, {Original: outcome.Saved}}}).Edits
		copy.Outcomes[i].Original, copy.Outcomes[i].Saved = original[0].Original, original[1].Original
		copy.Outcomes[i].PreviewColumns = append([]string(nil), outcome.PreviewColumns...)
		copy.Outcomes[i].Previews = append([]contract.DataCell(nil), outcome.Previews...)
		for j, preview := range outcome.Previews {
			if preview.Value != nil {
				v := preview.Value.Clone()
				copy.Outcomes[i].Previews[j].Value = &v
			}
		}
	}
	return &copy
}

// Bound copied request memory before cloning. Statement/JSON bounds are checked
// again after native typed literals and optimistic guards have been generated.
func dataEditBounds(edits []contract.DataEdit) error {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return contract.ErrDataEditLimit
	}
	total := 0
	for _, edit := range edits {
		if len(edit.Values) > 512 {
			return contract.ErrDataEditLimit
		}
		for name, v := range edit.Values {
			total += len(name) + len(v.Text) + len(v.Bytes)
		}
		if original := edit.Original; original != nil {
			if len(original.Columns) > 512 || len(original.Fingerprints) > 512 || len(original.Original) > 512 || len(original.Ref.Columns) > 512 || len(original.Ref.Values) > 512 || len(original.Ref.TypedValues) > 512 {
				return contract.ErrDataEditLimit
			}
			total += len(original.Metadata)
			for _, names := range [][]string{original.Columns, original.Ref.Columns} {
				for _, name := range names {
					total += len(name)
				}
			}
			for _, fp := range original.Fingerprints {
				if fp != nil {
					total += len(*fp)
				}
			}
			for _, values := range [][]contract.Cell{original.Original, original.Ref.TypedValues} {
				for _, v := range values {
					total += len(v.Text) + len(v.Bytes)
				}
			}
			for _, v := range original.Ref.Values {
				total += len(v)
			}
		}
		if total > maxRequestBytes {
			return contract.ErrDataEditLimit
		}
	}
	return nil
}

func (d *dataDocument) save(ctx context.Context, id int64, database, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	out := &contract.DataEditResult{RefreshRequired: true}
	if err := dataEditBounds(edits); err != nil {
		return out, err
	}
	// Own every original key, value, map and fingerprint before any admission
	// wait. Later caller edits cannot change this original native transaction.
	edits = contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: edits}).Edits
	child, done, err := d.admission(ctx, id, database, name)
	if err != nil {
		return out, err
	}
	defer done()
	select {
	case <-child.Done():
		return out, child.Err()
	case d.writes <- struct{}{}:
	}
	defer func() { <-d.writes }()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return out, contract.ErrRevoked
	}
	if d.unknown != nil {
		original := copyDataResult(d.unknown)
		d.mu.Unlock()
		return original, contract.ErrDataCommitUncertain
	}
	d.mu.Unlock()
	t, err := d.s.dataTable(child, id, database, name, d.object)
	if err != nil {
		return out, err
	}
	batch, err := d.s.dataEditBatch(t, edits)
	if err != nil {
		return out, err
	}
	out, err = d.s.executeDataBatch(child, t, edits, batch)
	if out.CommitUncertain || out.Incomplete {
		d.mu.Lock()
		d.unknown = copyDataResult(out)
		d.mu.Unlock()
	}
	// A write returns its original complete/unknown receipt even when Close
	// raced with execution. Only read results are discarded on document close.
	return out, err
}
