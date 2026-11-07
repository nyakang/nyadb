package dataworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

// Workers own these copies: UI drafts and page boundaries can change while a
// service is waiting for the server or acknowledging cancellation.
func cloneBytes(values [][]byte) [][]byte {
	if values == nil {
		return nil
	}
	out := make([][]byte, len(values))
	for i, value := range values {
		if value != nil {
			out[i] = make([]byte, len(value))
			copy(out[i], value)
		}
	}
	return out
}
func cloneRowRef(ref contract.RowRef) contract.RowRef {
	ref.Columns = append([]string(nil), ref.Columns...)
	ref.Values = cloneBytes(ref.Values)
	ref.TypedValues = cloneCells(ref.TypedValues)
	return ref
}
func cloneCursor(cursor *contract.DataCursor) *contract.DataCursor {
	if cursor == nil {
		return nil
	}
	out := *cursor
	out.Columns = append([]string(nil), cursor.Columns...)
	out.Descending = append([]bool(nil), cursor.Descending...)
	out.Values = cloneBytes(cursor.Values)
	return &out
}
func cloneSnapshot(snapshot *contract.RowSnapshot) *contract.RowSnapshot {
	if snapshot == nil {
		return nil
	}
	out := *snapshot
	out.Ref = cloneRowRef(snapshot.Ref)
	out.Original = cloneCells(snapshot.Original)
	out.Columns = append([]string(nil), snapshot.Columns...)
	if snapshot.Fingerprints != nil {
		out.Fingerprints = make([]*string, len(snapshot.Fingerprints))
	}
	for i, value := range snapshot.Fingerprints {
		if value != nil {
			copy := *value
			out.Fingerprints[i] = &copy
		}
	}
	return &out
}
func cloneValues(values map[string]contract.DataValue) map[string]contract.DataValue {
	if values == nil {
		return nil
	}
	out := make(map[string]contract.DataValue, len(values))
	for key, value := range values {
		value.Bytes = append([]byte(nil), value.Bytes...)
		out[key] = value
	}
	return out
}
func cloneEditResult(result *contract.DataEditResult) *contract.DataEditResult {
	if result == nil {
		return nil
	}
	out := *result
	out.Saved = cloneEdits(result.Saved)
	out.Outcomes = append([]contract.DataEditOutcome(nil), result.Outcomes...)
	for i := range out.Outcomes {
		x := &out.Outcomes[i]
		x.Original, x.Saved = cloneSnapshot(x.Original), cloneSnapshot(x.Saved)
		x.PreviewColumns = append([]string(nil), x.PreviewColumns...)
		x.Previews = clonePreviews(x.Previews)
	}
	return &out
}

func cloneCells(values []contract.Cell) []contract.Cell {
	if values == nil {
		return nil
	}
	out := make([]contract.Cell, len(values))
	for i, v := range values {
		out[i] = v.Clone()
	}
	return out
}
func clonePreviews(values []contract.DataCell) []contract.DataCell {
	if values == nil {
		return nil
	}
	out := append([]contract.DataCell(nil), values...)
	for i := range out {
		if out[i].Value != nil {
			v := out[i].Value.Clone()
			out[i].Value = &v
		}
	}
	return out
}
func cloneEdits(values []contract.Edit) []contract.Edit {
	if values == nil {
		return nil
	}
	out := append([]contract.Edit(nil), values...)
	for i := range out {
		out[i].Original = cloneCells(out[i].Original)
		out[i].Identity.Columns = append([]string(nil), out[i].Identity.Columns...)
		out[i].Identity.Values = cloneCells(out[i].Identity.Values)
		m := make(map[string]contract.Cell, len(out[i].Values))
		for k, v := range out[i].Values {
			m[k] = v.Clone()
		}
		out[i].Values = m
	}
	return out
}
