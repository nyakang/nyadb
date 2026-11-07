package dameng

import (
	"context"
	"database/sql"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func setOutcomePreview(out *contract.DataEditOutcome, t *contract.TableStructure, row []contract.Cell) {
	out.PreviewColumns = nil
	out.Previews = nil
	for i, c := range row {
		out.PreviewColumns = append(out.PreviewColumns, t.Columns[i].Name)
		out.Previews = append(out.Previews, dataCell(boundedCell(c, 1024)))
	}
}

func optionalDataRow(ctx context.Context, q metadataQueryer, t *contract.TableStructure, key []string, ref contract.RowRef) ([]contract.Cell, bool, error) {
	where, args, err := rowWhere(t, key, ref)
	if err != nil {
		return nil, false, err
	}
	var count int64
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(t.Object)+" WHERE "+where, args...).Scan(&count); err != nil {
		return nil, false, err
	}
	if count == 0 {
		return nil, false, nil
	}
	if count != 1 {
		return nil, false, contract.ErrDataConflict
	}
	row, err := readFullRowAt(ctx, q, t, key, ref)
	return row, err == nil, err
}

// Verification compares complete current state while SHARE excludes all writes.
// It issues no mutation and cannot establish which actor produced an identical
// state. Unidentifiable inserts, no-op updates and mixed states stay indeterminate.
func (s *Session) verifyData(ctx context.Context, id int64, schema, name string, input *contract.DataEditResult) (out *contract.DataVerification, err error) {
	o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
	if err != nil {
		return nil, err
	}
	if input == nil || len(input.Outcomes) == 0 || len(input.Outcomes) > 1000 {
		return nil, connections.ErrInvalid
	}
	originals, saved := make([]contract.DataEdit, len(input.Outcomes)), make([]contract.DataEdit, len(input.Outcomes))
	for i, v := range input.Outcomes {
		originals[i] = contract.DataEdit{Kind: v.Kind, Original: v.Original}
		saved[i] = contract.DataEdit{Original: v.Saved}
	}
	originals, err = cloneDataEdits(originals)
	if err != nil {
		return nil, err
	}
	saved, err = cloneDataEdits(saved)
	if err != nil {
		return nil, err
	}
	owned := make([]contract.DataEditOutcome, len(originals))
	for i := range owned {
		owned[i] = contract.DataEditOutcome{Kind: originals[i].Kind, Original: originals[i].Original, Saved: saved[i].Original}
	}
	out = &contract.DataVerification{Status: "indeterminate"}
	err = s.completeDataRead(ctx, o, func(ctx context.Context, q *sql.Conn, t *contract.TableStructure) error {
		key, e := editableTableAt(ctx, q, t)
		if e != nil {
			return e
		}
		identity, e := tableIdentityAt(ctx, q, o)
		if e != nil {
			return e
		}
		for _, v := range owned {
			switch v.Kind {
			case "insert":
				if v.Original != nil {
					return connections.ErrInvalid
				}
			case "update", "delete":
				if v.Original == nil || v.Kind == "delete" && v.Saved != nil {
					return connections.ErrInvalid
				}
			default:
				return connections.ErrInvalid
			}
			for _, p := range []*contract.RowSnapshot{v.Original, v.Saved} {
				if p != nil {
					if e = validateDataSnapshot(s, t, key, identity, p); e != nil {
						return e
					}
				}
			}
		}
		recovered := &contract.DataEditResult{Applied: len(owned), Verified: true}
		budget := dataBatchBytes
		for _, v := range owned {
			state := "indeterminate"
			var current []contract.Cell
			var present, oldPresent bool
			var old []contract.Cell
			if v.Saved != nil {
				current, present, e = optionalDataRow(ctx, q, t, key, v.Saved.Ref)
				if e != nil {
					return e
				}
			}
			if v.Original != nil {
				if v.Saved != nil && sameDataRef(v.Saved.Ref, v.Original.Ref) {
					old, oldPresent = current, present
				} else {
					old, oldPresent, e = optionalDataRow(ctx, q, t, key, v.Original.Ref)
					if e != nil {
						return e
					}
				}
			}
			matchesSaved := present && v.Saved != nil && sameDataRow(current, v.Saved.Original)
			if e = chargeDataRow(&budget, current); e != nil {
				return e
			}
			if e = chargeDataRow(&budget, old); e != nil {
				return e
			}
			matchesOld := oldPresent && v.Original != nil && sameDataRow(old, v.Original.Original)
			switch v.Kind {
			case "insert":
				if matchesSaved {
					state = "applied"
				} else if v.Saved != nil && !present {
					state = "not_applied"
				}
			case "delete":
				if !oldPresent {
					state = "applied"
				} else if matchesOld {
					state = "not_applied"
				}
			case "update":
				if v.Saved != nil {
					if sameDataRef(v.Saved.Ref, v.Original.Ref) {
						if matchesSaved && !matchesOld {
							state = "applied"
						} else if matchesOld && !matchesSaved {
							state = "not_applied"
						}
					} else {
						if matchesSaved && !oldPresent {
							state = "applied"
						} else if matchesOld && !present {
							state = "not_applied"
						}
					}
				}
			}
			out.States = append(out.States, state)
			result := contract.DataEditOutcome{Kind: v.Kind, Original: v.Original}
			if state == "applied" && v.Kind != "delete" {
				result.Saved = s.fullSnapshot(t, key, current, identity)
				setOutcomePreview(&result, t, current)
			}
			recovered.Outcomes = append(recovered.Outcomes, result)
			recovered.Saved = append(recovered.Saved, savedEdit(contract.DataEdit{Kind: v.Kind, Original: v.Original}, result.Saved))
		}
		first := out.States[0]
		all := true
		for _, state := range out.States {
			all = all && state == first
		}
		if all {
			out.Status = first
		}
		if out.Status == "applied" {
			out.Recovered = recovered
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
