package dataworkspace

import (
	"bytes"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dataCompleteRowMatches(cached *contract.RowSnapshot, row *contract.DataRow) bool {
	if row == nil || cached == nil || row.Snapshot == nil {
		return false
	}
	if row.PreviewSnapshot == nil {
		return reflect.DeepEqual(cached, row.Snapshot)
	}
	full := row.Snapshot
	if cached.Mode != "values" || full.Mode != cached.Mode || cached.Metadata == "" || (!reflect.DeepEqual(cached, row.PreviewSnapshot) && !reflect.DeepEqual(cached, full)) ||
		!reflect.DeepEqual(cached.Ref, full.Ref) || cached.Metadata != full.Metadata || !reflect.DeepEqual(cached.Columns, full.Columns) ||
		!reflect.DeepEqual(cached.Fingerprints, full.Fingerprints) || len(full.Columns) == 0 || len(cached.Original) != len(full.Columns) || len(full.Original) != len(full.Columns) || len(row.Values) != len(full.Columns) {
		return false
	}
	budget := 0
	for i, name := range full.Columns {
		cell := full.Original[i]
		value, ok := row.Values[name]
		if !ok || cell.Kind == "" || cell.Truncated || value.Default || value.Kind != cell.Kind || value.Text != cell.Text ||
			!bytes.Equal(value.Bytes, cell.Bytes) || value.Null != (cell.Kind == contract.Null) || value.Binary != (cell.Kind == contract.Binary) {
			return false
		}
		budget += len(cell.Text) + len(cell.Bytes)
		if budget > contract.DataEditMaxBytes {
			return false
		}
	}
	return true
}
