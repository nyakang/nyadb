package dataworkspace

import (
	"bytes"
	"context"
	"reflect"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *State) canReplaceDataRecord(d *Document, row int) bool {
	if d == nil || d.closed || d.object.ProfileID != d.profileID || d.profileID <= 0 || !d.editable() || d.Busy() || d.HasDraft() || d.edit.Uncertain != nil || d.transfer.Uncertain || dataTransactionUnavailable(d) || dataTransactionActive(d) || !s.canUseSession(d) || d.profileID != s.session().ProfileID || !d.allowsDataEdit("replace") || s.runtimeFor(d).PreviewEdits == nil || s.runtimeFor(d).SubmitEdits == nil || s.rowReaderFor(d) == nil || row < 0 || row >= len(d.page.Rows) || row >= len(d.page.RowSnapshots) || d.page.RowSnapshots[row] == nil || row >= len(d.page.RowRefs) || d.page.RowRefs[row] == nil || d.page.RowSnapshots[row].Ref.Object != d.object || d.page.ReadOnlyRows[row] {
		return false
	}
	return len(d.page.Columns) > 0 && len(d.page.ColumnMetadata) == len(d.page.Columns) && mysqlDataRowKey(d, row) != ""
}

// ReplaceRecord explicitly acquires the complete original record before creating
// a replacement draft. Ordinary cell edits never infer this operation.
func (s *State) ReplaceRecord(services ui.Services, d *Document, row int) {
	if !s.canReplaceDataRecord(d, row) {
		return
	}
	s.finishMySQLCellEdit(d, false)
	ctx, cancel := context.WithCancel(context.Background())
	d.edit.Cancel, d.edit.Busy, d.edit.Err = cancel, true, ""
	clearDraftHistory(d)
	d.edit.Request++
	request, gen, profile := d.edit.Request, s.sessionFor(d).Generation, d.profileID
	ref := cloneRowRef(*d.page.RowRefs[row])
	original := cloneSnapshot(d.page.RowSnapshots[row])
	key, database, table := mysqlDataRowKey(d, row), d.database, d.table
	reader := s.rowReaderFor(d)
	out := make(chan mysqlEditResult, 1)
	d.edit.Results = out
	go func() {
		value, err := reader(ctx, profile, database, table, ref)
		if err == nil {
			err = ctx.Err()
		}
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Row: value, TargetRow: row, TargetKey: key, ReplacementSource: original, Err: err}
		services.Invalidate()
	}()
}

func completeReplacementValues(page *contract.DataPage, row *contract.DataRow) bool {
	if row == nil || row.Snapshot == nil || row.Snapshot.Mode != "values" || !reflect.DeepEqual(row.Columns, page.ColumnMetadata) || !reflect.DeepEqual(row.Snapshot.Columns, page.Columns) || len(row.Values) != len(page.Columns) || len(row.Snapshot.Original) != len(page.Columns) {
		return false
	}
	size := 0
	for i, name := range page.Columns {
		v, ok := row.Values[name]
		cell := row.Snapshot.Original[i]
		if !ok || cell.Truncated || cell.Kind == "" || v.Default || cell.Kind != v.Kind || cell.Text != v.Text || !bytes.Equal(cell.Bytes, v.Bytes) || v.Null != (cell.Kind == contract.Null) || v.Binary != (cell.Kind == contract.Binary) {
			return false
		}
		size += len(v.Text) + len(v.Bytes)
		if size > contract.DataEditMaxBytes {
			return false
		}
	}
	return true
}

func (s *State) consumeDataReplacement(services ui.Services, d *Document, r mysqlEditResult) {
	row := r.TargetRow
	if !s.canReplaceDataRecord(d, row) || mysqlDataRowKey(d, row) != r.TargetKey || !reflect.DeepEqual(d.page.RowSnapshots[row], r.ReplacementSource) || !dataCompleteRowMatches(r.ReplacementSource, r.Row) || !completeReplacementValues(d.page, r.Row) {
		d.edit.Err = contract.ErrDataConflict.Error()
		return
	}
	done := beginDraftChange(d)
	defer done()
	before := clonePreviews(d.page.Rows[row])
	if d.edit.Drafts == nil {
		d.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	d.edit.Drafts[r.TargetKey] = &mysqlRowDraft{Key: r.TargetKey, Row: row, Kind: "replace", Original: cloneSnapshot(r.Row.Snapshot), Before: before, Values: cloneValues(r.Row.Values)}
	d.page.RowSnapshots[row] = cloneSnapshot(r.Row.Snapshot)
	for i, name := range d.page.Columns {
		d.page.Rows[row][i] = mysqlDataValuePreview(r.Row.Values[name])
	}
	d.selectedRow = row
	d.selection.Clear()
	d.selection.Add(row)
	mysqlDataClearColumnSelection(d)
	d.recordFormMode, d.toolbar.dock = true, ""
	d.edit.Err = ""
	services.Invalidate()
}
