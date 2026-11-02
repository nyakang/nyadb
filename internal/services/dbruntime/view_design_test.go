package dbruntime_test

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"testing"
)

type viewHandle struct {
	handle
	apply func(context.Context, contract.ViewDesignRequest) contract.ViewDesignOutcome
}

func (h *viewHandle) Generation() uint64 { return 973 }
func (h *viewHandle) ViewDesign() contract.ViewDesignOperations {
	return contract.ViewDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return t.Generation == 973 }, Apply: h.apply}
}
func TestViewDesignTranslatesClonesAndPreservesLateRenameReceipt(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &viewHandle{handle: handle{id: 1}}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "aux", Kind: "view", Name: "v"}, Generation: s.Generation()}
	r := contract.ViewDesignRequest{Original: contract.ViewDesignSnapshot{Target: target, Schema: schemaddl.ViewSnapshot{Engine: "sqlite", Revision: "source", Exists: true, View: schemaddl.View{Name: "v", Columns: []string{"id"}}}}, Action: "rename", Desired: schemaddl.View{Name: "renamed", Columns: []string{"id"}}}
	h.apply = func(_ context.Context, request contract.ViewDesignRequest) contract.ViewDesignOutcome {
		if request.Original.Target.Generation != 973 || request.Original.Target.Object != target.Object {
			t.Fatal("scope not translated")
		}
		request.Original.Schema.View.Columns[0] = "driver mutation"
		request.Desired.Columns[0] = "another driver mutation"
		a := request.Original.Clone()
		a.Target.Object.Name = request.Desired.Name
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
		return contract.ViewDesignOutcome{State: "committed", Verified: true, Actual: &a}
	}
	ops := m.ViewDesign(1)
	if !ops.CanApply(target) {
		t.Fatal("binding disabled")
	}
	out := ops.Apply(context.Background(), r)
	expected := target
	expected.Object.Name = "renamed"
	if out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != expected || r.Original.Schema.View.Columns[0] != "id" || r.Desired.Columns[0] != "id" {
		t.Fatal("clone or late receipt lost", out)
	}
	if ops.CanApply(target) || !errors.Is(ops.Apply(context.Background(), r).Error, contract.ErrRevoked) {
		t.Fatal("revoked view binding used")
	}
}
func TestViewDesignRejectsForeignScopeAndWrongExistenceReceipt(t *testing.T) {
	for _, change := range []string{"profile", "generation", "schema", "name", "exists"} {
		t.Run(change, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := &viewHandle{handle: handle{id: 1}}
			h.apply = func(_ context.Context, r contract.ViewDesignRequest) contract.ViewDesignOutcome {
				a := r.Original.Clone()
				a.Target.Object.Name = r.Desired.Name
				a.Schema.Exists = true
				switch change {
				case "profile":
					a.Target.Object.ProfileID++
				case "generation":
					a.Target.Generation++
				case "schema":
					a.Target.Object.Schema = "other"
				case "name":
					a.Target.Object.Name = "other"
				case "exists":
					a.Schema.Exists = false
				}
				return contract.ViewDesignOutcome{State: "committed", Verified: true, Actual: &a}
			}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			s, err := m.ConnectSession(context.Background(), profile(1), "")
			if err != nil {
				t.Fatal(err)
			}
			r := contract.ViewDesignRequest{Original: contract.ViewDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Kind: "view", Name: "v"}, Generation: s.Generation()}}, Action: "rename", Desired: schemaddl.View{Name: "renamed"}}
			out := m.ViewDesign(1).Apply(context.Background(), r)
			if out.State != "committed" || out.Verified || out.Actual != nil || out.Error == nil {
				t.Fatal("unsafe receipt trusted", out)
			}
		})
	}
}
