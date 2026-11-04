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

type dropHandle struct {
	handle
	drop func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome
}

func (h *dropHandle) Generation() uint64 { return 973 }
func (h *dropHandle) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{CanDrop: func(t contract.SchemaDesignTarget) bool { return t.Generation == 973 }, Drop: h.drop}
}

func TestSchemaDropTranslatesCopiesAndRetainsLateOriginalReceipt(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &dropHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "main", Kind: "table", Name: "t"}, Generation: s.Generation()}
	original := contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Exists: true, Table: schemaddl.Table{Name: "t", Clauses: []schemaddl.Clause{{Name: "id"}}}}}
	h.drop = func(_ context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
		if r.Target.Generation != 973 || r.Target.Object != target.Object {
			t.Fatal("untranslated drop", r.Target)
		}
		r.Schema.Table.Clauses[0].Name = "adapter mutation"
		r.Schema.Exists = false
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
		return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r}
	}
	ops := m.SchemaDesign(1)
	if !ops.CanDrop(target) {
		t.Fatal("public drop availability lost")
	}
	out := ops.Drop(context.Background(), original)
	if out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != target || original.Schema.Table.Clauses[0].Name != "id" {
		t.Fatalf("drop receipt/copy: %+v", out)
	}
	if ops.CanDrop(target) {
		t.Fatal("revoked drop remains enabled")
	}
	if out = ops.Drop(context.Background(), original); !errors.Is(out.Error, contract.ErrRevoked) {
		t.Fatal(out)
	}
}

func TestSchemaDropRejectsAnotherScopeAndTableStillPresentReceipt(t *testing.T) {
	for _, mismatch := range []string{"profile", "generation", "name", "exists"} {
		t.Run(mismatch, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := &dropHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
			h.drop = func(_ context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
				r.Schema.Exists = false
				switch mismatch {
				case "profile":
					r.Target.Object.ProfileID++
				case "generation":
					r.Target.Generation++
				case "name":
					r.Target.Object.Name = "other"
				case "exists":
					r.Schema.Exists = true
				}
				return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r}
			}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			s, err := m.ConnectSession(context.Background(), profile(1), "")
			if err != nil {
				t.Fatal(err)
			}
			original := contract.SchemaDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Kind: "table", Name: "t"}, Generation: s.Generation()}}
			out := m.SchemaDesign(1).Drop(context.Background(), original)
			if out.State != "committed" || out.Verified || out.Actual != nil || out.Error == nil {
				t.Fatalf("unsafe receipt trusted: %+v", out)
			}
		})
	}
}
