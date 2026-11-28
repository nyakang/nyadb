package dataworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestOriginalPageOperationsNarrowRuntimeAndPreventWholeRowDrafts(t *testing.T) {
	s, d, tt, _ := editingTestWorkspace(t)
	defer s.CloseAll()
	d.page.EditKinds = []string{"update"}
	tt.Frame()
	n := len(d.page.Rows)
	s.addMySQLDataRecord(ui.Services{}, d)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if len(d.page.Rows) != n || d.HasDraft() || mysqlDataCanDeleteSelection(d, 0) || !mysqlDataCanEdit(d, 0, 1) {
		t.Fatal("page mode failed to narrow runtime")
	}
	d.boundRuntime = &Runtime{EditKinds: []string{"insert"}}
	if d.allowsDataEdit("update") || d.allowsDataEdit("insert") {
		t.Fatal("page permissions widened runtime")
	}
	d.boundRuntime = nil
	d.page.EditKinds = []string{}
	if mysqlDataCanEdit(d, 0, 1) || d.allowsDataEdit("delete") {
		t.Fatal("empty page operations inherited defaults")
	}
	d.page.EditKinds = nil
	if !mysqlDataCanEdit(d, 0, 1) || !d.allowsDataEdit("insert") || !d.allowsDataEdit("delete") {
		t.Fatal("nil page operations lost compatibility")
	}
}
func TestPublishedPageOwnsOperationPermissionSlice(t *testing.T) {
	s, d, tt, _ := editingTestWorkspace(t)
	defer s.CloseAll()
	original := []string{"update"}
	page := *d.page
	page.EditKinds = original
	// Supply the real private result channel to exercise page publication.
	s.AdvancedLoader = nil
	s.Loader = func(context.Context, int64, string, string, int64, int) (*contract.DataPage, error) {
		return &page, nil
	}
	s.Load(ui.Services{}, d, true)
	waitMySQLData(t, s, d, tt)
	original[0] = "delete"
	if !d.allowsDataEdit("update") || d.allowsDataEdit("delete") {
		t.Fatal("published operation list followed provider slice")
	}
}
