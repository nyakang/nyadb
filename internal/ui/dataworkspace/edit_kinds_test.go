package dataworkspace

import (
	"context"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestBoundDataEditKindsOwnsProviderModeSnapshot(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	lease := &testDocumentLease{cancel: cancel, runtime: Runtime{NativeReceipts: true, EditKinds: []string{"update"}, EditValueKinds: []contract.CellKind{contract.Integer}, Capabilities: contract.Capabilities{Documents: true, EditableTables: true}}}
	lease.runtime.Saver = func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
		return nil, nil
	}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }, Runtime: func() Runtime {
		return Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return lease, nil }}
	}})
	defer s.CloseAll()
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("label", "owned")
	d := s.OpenObject(1, object, lease.runtime.Capabilities)
	lease.runtime.EditKinds[0] = "delete"
	lease.runtime.NativeReceipts = false
	if !d.boundRuntime.NativeReceipts {
		t.Fatal("native receipt mode followed a later provider")
	}
	lease.runtime.EditValueKinds[0] = contract.Text
	if d.boundRuntime.EditValueKinds[0] != contract.Integer {
		t.Fatal("editor kinds followed later provider state")
	}
	if !d.allowsDataEdit("update") || d.allowsDataEdit("delete") {
		t.Fatal("document mutation kinds followed later provider state")
	}
	d.boundRuntime.EditKinds[0] = "insert"
	if lease.runtime.EditKinds[0] != "delete" {
		t.Fatal("document mode snapshot mutated its original provider")
	}
}

func TestBoundDataEditKindsLimitCommandsWithoutInventingEntityInsertOrDelete(t *testing.T) {
	s, d, tt, _ := editingTestWorkspace(t)
	defer s.CloseAll()
	d.boundRuntime = &Runtime{EditKinds: []string{"update"}}
	originalRows := clonePreviews(d.page.Rows[0])
	count := len(d.page.Rows)
	tt.Frame()
	s.addMySQLDataRecord(ui.Services{}, d)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if len(d.page.Rows) != count || mysqlDataHasDraft(d) || mysqlDataCanDeleteSelection(d, 0) {
		t.Fatal("update-only provider admitted a whole-entity mutation")
	}
	if !mysqlDataCanEdit(d, 0, 1) {
		t.Fatal("update-only provider lost existing property editing")
	}
	s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Kind: contract.Text, Text: "changed"})
	if !mysqlDataHasDraft(d) || reflect.DeepEqual(d.page.Rows[0], originalRows) {
		t.Fatal("allowed property update did not reach the sole grid draft")
	}
	s.discardMySQLData(d)
	d.boundRuntime.EditKinds = []string{}
	if mysqlDataCanEdit(d, 0, 1) || d.allowsDataEdit("insert") || d.allowsDataEdit("delete") {
		t.Fatal("explicitly empty edit kinds inherited defaults")
	}
	d.boundRuntime.EditKinds = nil
	if !mysqlDataCanEdit(d, 0, 1) || !d.allowsDataEdit("insert") || !mysqlDataCanDeleteSelection(d, 0) {
		t.Fatal("legacy/default provider mutation availability changed")
	}
}
