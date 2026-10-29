package dataworkspace

import mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"

// Workers own these copies: UI drafts and page boundaries can change while a
// service is waiting for the server or acknowledging cancellation.
func cloneBytes(values [][]byte) [][]byte {
	if values == nil {
		return nil
	}
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = append([]byte(nil), value...)
	}
	return out
}
func cloneRowRef(ref mysqlsvc.MySQLRowRef) mysqlsvc.MySQLRowRef {
	ref.Columns = append([]string(nil), ref.Columns...)
	ref.Values = cloneBytes(ref.Values)
	return ref
}
func cloneCursor(cursor *mysqlsvc.MySQLDataCursor) *mysqlsvc.MySQLDataCursor {
	if cursor == nil {
		return nil
	}
	out := *cursor
	out.Columns = append([]string(nil), cursor.Columns...)
	out.Descending = append([]bool(nil), cursor.Descending...)
	out.Values = cloneBytes(cursor.Values)
	return &out
}
func cloneSnapshot(snapshot *mysqlsvc.MySQLRowSnapshot) *mysqlsvc.MySQLRowSnapshot {
	if snapshot == nil {
		return nil
	}
	out := *snapshot
	out.Ref = cloneRowRef(snapshot.Ref)
	out.Columns = append([]string(nil), snapshot.Columns...)
	out.Fingerprints = make([]*string, len(snapshot.Fingerprints))
	for i, value := range snapshot.Fingerprints {
		if value != nil {
			copy := *value
			out.Fingerprints[i] = &copy
		}
	}
	return &out
}
func cloneValues(values map[string]mysqlsvc.MySQLDataValue) map[string]mysqlsvc.MySQLDataValue {
	if values == nil {
		return nil
	}
	out := make(map[string]mysqlsvc.MySQLDataValue, len(values))
	for key, value := range values {
		value.Bytes = append([]byte(nil), value.Bytes...)
		out[key] = value
	}
	return out
}
func cloneEditResult(result *mysqlsvc.MySQLDataEditResult) *mysqlsvc.MySQLDataEditResult {
	if result == nil {
		return nil
	}
	out := *result
	out.Outcomes = append([]mysqlsvc.MySQLDataEditOutcome(nil), result.Outcomes...)
	for i := range out.Outcomes {
		x := &out.Outcomes[i]
		x.Original, x.Saved = cloneSnapshot(x.Original), cloneSnapshot(x.Saved)
		x.PreviewColumns = append([]string(nil), x.PreviewColumns...)
		x.Previews = append([]mysqlsvc.MySQLDataCell(nil), x.Previews...)
	}
	return &out
}
