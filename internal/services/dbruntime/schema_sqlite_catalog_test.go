package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type nativeCatalogHandle struct {
	handle
	load func(context.Context, contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error)
}

func (h *nativeCatalogHandle) Generation() uint64 { return 1 }
func (h *nativeCatalogHandle) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{Snapshot: h.load}
}

func TestSQLiteCatalogBindingOwnsNativeGenerationAndCompleteRecords(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	p, _ := connections.Defaults(connections.RQLite)
	p.ID, p.Name = 1, "original native catalog"
	var original contract.SchemaDesignSnapshot
	h := &nativeCatalogHandle{handle: handle{id: 1, caps: contract.Capabilities{Metadata: true}}}
	h.load = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		if target.Generation != 1 || target.Object.ProfileID != 1 || target.Object.Catalog != "main" || target.Object.Schema != "" {
			t.Fatal("public catalog scope was not translated at the runtime boundary", target)
		}
		original = contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "native-original-seal", SQLite: &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "index", Name: "implicit", Table: "t", Root: 3, SQLNull: true}, {Type: "table", Name: "t", Table: "t", Root: 2, SQL: "CREATE TABLE t(id INTEGER PRIMARY KEY)"}}}, Table: schemaddl.Table{Name: "t"}}}
		return original, nil
	}
	m.Register(connections.RQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if _, err := m.ConnectSession(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	h.handle = handle{id: 1, caps: contract.Capabilities{Metadata: true}}
	s, err := m.ConnectSession(context.Background(), p, "")
	if err != nil || s.Generation() == 1 {
		t.Fatal("fixture did not distinguish native and published generations", err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Kind: "table", Name: "t"}, Generation: s.Generation()}
	ops := m.SchemaDesign(1)
	if ops.Snapshot == nil || ops.Apply != nil || ops.CanApply != nil || ops.Drop != nil || ops.Copy != nil || ops.Rename != nil {
		t.Fatal("readable native catalog acquired write methods")
	}
	snapshot, err := ops.Snapshot(context.Background(), target)
	if err != nil || snapshot.Target != target || snapshot.Schema.Revision != "native-original-seal" {
		t.Fatal("original catalog seal or published target changed", err)
	}
	snapshot.Schema.SQLite.Entries[0].SQLNull = false
	snapshot.Schema.SQLite.Entries[1].SQL = "changed"
	if !original.Schema.SQLite.Entries[0].SQLNull || original.Schema.SQLite.Entries[1].SQL == "changed" {
		t.Fatal("runtime exposed provider-owned catalog records")
	}
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Snapshot(context.Background(), target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original closed catalog scope remained usable", err)
	}
}
