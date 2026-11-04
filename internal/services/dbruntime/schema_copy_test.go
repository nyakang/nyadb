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

type copyHandle struct {
	handle
	copy func(context.Context, contract.SchemaCopyRequest) contract.SchemaDesignOutcome
}

func (h *copyHandle) Generation() uint64 { return 973 }
func (h *copyHandle) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{CanCopy: func(t contract.SchemaDesignTarget) bool { return t.Generation == 973 }, Copy: h.copy}
}

func copyRuntime(t *testing.T, h *copyHandle) (*dbruntime.Manager, contract.SchemaCopyRequest) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return m, contract.SchemaCopyRequest{Original: contract.SchemaDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "aux", Kind: "table", Name: "t"}, Generation: s.Generation()}, Schema: schemaddl.Snapshot{Exists: true, Table: schemaddl.Table{Name: "t", Clauses: []schemaddl.Clause{{Name: "id"}}}}}, Destination: "copied", WithData: true, RebindSelfReferences: true, PreserveRowID: true}
}

func TestSchemaCopyTranslatesClonesAndRetainsLateDestinationReceipt(t *testing.T) {
	h := &copyHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
	m, request := copyRuntime(t, h)
	h.copy = func(_ context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
		if r.Original.Target.Generation != 973 || r.Original.Target.Object != request.Original.Target.Object || r.Destination != "copied" || !r.WithData || !r.RebindSelfReferences || !r.PreserveRowID {
			t.Fatal("request translation lost identity/choices")
		}
		r.Original.Schema.Table.Clauses[0].Name = "adapter mutation"
		r.Original.Target.Object.Name = r.Destination
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
		return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r.Original}
	}
	ops := m.SchemaDesign(1)
	if !ops.CanCopy(request.Original.Target) {
		t.Fatal("copy binding disabled")
	}
	out := ops.Copy(context.Background(), request)
	expected := request.Original.Target
	expected.Object.Name = "copied"
	if out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != expected || request.Original.Schema.Table.Clauses[0].Name != "id" {
		t.Fatalf("copy receipt: %+v", out)
	}
	if ops.CanCopy(request.Original.Target) || !errors.Is(ops.Copy(context.Background(), request).Error, contract.ErrRevoked) {
		t.Fatal("revoked binding accepted copy")
	}
}

func TestSchemaCopyRejectsMismatchedOrAbsentDestinationReceipt(t *testing.T) {
	for _, field := range []string{"profile", "generation", "catalog", "schema", "name", "exists"} {
		t.Run(field, func(t *testing.T) {
			h := &copyHandle{handle: handle{id: 1, caps: contract.Capabilities{Objects: true}}}
			m, request := copyRuntime(t, h)
			h.copy = func(_ context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
				a := r.Original.Clone()
				a.Target.Object.Name = r.Destination
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
				case "exists":
					a.Schema.Exists = false
				}
				return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &a}
			}
			out := m.SchemaDesign(1).Copy(context.Background(), request)
			if out.State != "committed" || out.Verified || out.Actual != nil || out.Error == nil {
				t.Fatalf("foreign destination receipt trusted: %+v", out)
			}
		})
	}
}
