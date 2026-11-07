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

type renameHandle struct {
	handle
	rename func(context.Context, contract.SchemaRenameRequest) contract.SchemaDesignOutcome
}

func (h *renameHandle) Generation() uint64 { return 973 }
func (h *renameHandle) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{CanRename: func(t contract.SchemaDesignTarget) bool { return t.Generation == 973 }, Rename: h.rename}
}

func renameRuntime(t *testing.T, h *renameHandle) (*dbruntime.Manager, contract.SchemaRenameRequest) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return m, contract.SchemaRenameRequest{Original: contract.SchemaDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "aux", Kind: "table", Name: "t"}, Generation: s.Generation()}, Schema: schemaddl.Snapshot{Exists: true, Table: schemaddl.Table{Name: "t", Clauses: []schemaddl.Clause{{Name: "id"}}}}}, Destination: "renamed"}
}

func TestSchemaRenameTranslatesCopiesAndRetainsLateOriginalReceipt(t *testing.T) {
	h := &renameHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
	m, request := renameRuntime(t, h)
	h.rename = func(_ context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
		if r.Original.Target.Generation != 973 || r.Original.Target.Object != request.Original.Target.Object || r.Destination != "renamed" {
			t.Fatal("rename translation lost captured scope")
		}
		r.Original.Schema.Table.Clauses[0].Name = "adapter mutation"
		r.Original.Target.Object.Name, r.Original.Schema.Table.Name = r.Destination, r.Destination
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
		return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r.Original}
	}
	ops := m.SchemaDesign(1)
	if !ops.CanRename(request.Original.Target) || !m.Availability(1, "rename_table", request.Original.Target).Enabled {
		t.Fatal("native rename unavailable")
	}
	out := ops.Rename(context.Background(), request)
	expected := request.Original.Target
	expected.Object.Name = "renamed"
	if out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != expected || request.Original.Schema.Table.Clauses[0].Name != "id" {
		t.Fatalf("captured rename receipt: %+v", out)
	}
	if ops.CanRename(request.Original.Target) || !errors.Is(ops.Rename(context.Background(), request).Error, contract.ErrRevoked) {
		t.Fatal("revoked binding accepted rename")
	}
}

func TestSchemaRenameRejectsForeignScopeAbsentOrMismatchedTableReceipt(t *testing.T) {
	for _, field := range []string{"profile", "generation", "catalog", "schema", "name", "kind", "exists", "table"} {
		t.Run(field, func(t *testing.T) {
			h := &renameHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
			m, request := renameRuntime(t, h)
			h.rename = func(_ context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
				a := r.Original.Clone()
				a.Target.Object.Name, a.Schema.Table.Name = r.Destination, r.Destination
				switch field {
				case "profile":
					a.Target.Object.ProfileID++
				case "generation":
					a.Target.Generation++
				case "catalog":
					a.Target.Object.Catalog = "other"
				case "schema":
					a.Target.Object.Schema = "other"
				case "name":
					a.Target.Object.Name = "t"
				case "kind":
					a.Target.Object.Kind = "view"
				case "exists":
					a.Schema.Exists = false
				case "table":
					a.Schema.Table.Name = "other"
				}
				return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &a}
			}
			out := m.SchemaDesign(1).Rename(context.Background(), request)
			if out.State != "committed" || out.Verified || out.Actual != nil || out.Error == nil {
				t.Fatalf("foreign rename receipt trusted: %+v", out)
			}
		})
	}
}
