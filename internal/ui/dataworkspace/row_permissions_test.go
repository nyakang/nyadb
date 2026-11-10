package dataworkspace

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestCapturedReadOnlyRowsBlockCellAndWholeSelectedDelete(t *testing.T) {
	s, d, tt, fixture := editingTestWorkspace(t)
	d.page.ReadOnlyRows = map[int]bool{0: true, 2: true}
	if mysqlDataCanEdit(d, 0, 1) || !mysqlDataCanEdit(d, 1, 1) || mysqlDataCanEdit(d, 2, 1) {
		t.Fatal("row permission leaked into unrelated records")
	}
	d.selection.Add(0)
	d.selection.Add(1)
	if mysqlDataCanDeleteSelection(d, 1) {
		t.Fatal("mixed read-only selection enabled delete")
	}
	s.deleteMySQLDataRecords(ui.Services{}, d, 1)
	if d.HasDraft() || len(fixture.writes) != 0 {
		t.Fatal("mixed selection partially staged or wrote")
	}
	s.EditCell(ui.Services{}, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "denied"})
	if d.HasDraft() {
		t.Fatal("direct read-only editing bypassed grid admission")
	}
	d.selection.Clear()
	d.selection.Add(1)
	if !mysqlDataCanDeleteSelection(d, 1) {
		t.Fatal("writable row disabled")
	}
	tt.Frame()
}
func TestRemovingOriginalDraftRowsReindexesPermissionsWithoutAliasing(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	original := map[int]bool{0: true, 2: true}
	d.page.ReadOnlyRows = original
	d.edit.Drafts = map[string]*mysqlRowDraft{"removed": {Row: 1, Kind: "delete"}}
	s.removeMySQLDataDraftRows(d, func(*mysqlRowDraft) bool { return true })
	if len(d.page.Rows) != 2 || !d.page.ReadOnlyRows[0] || !d.page.ReadOnlyRows[1] || len(d.page.ReadOnlyRows) != 2 {
		t.Fatal("remaining records lost captured permission ordinals")
	}
	if len(original) != 2 || !original[2] {
		t.Fatal("row removal mutated the original permission map")
	}
}
