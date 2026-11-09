package dbruntime_test

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"testing"
)

func (h *graphSchemaTestHandle) SupportsGraphSchemaManagement(o contract.ObjectRef) bool {
	return h.SupportsGraphSchemaPreparation(o)
}
func (h *graphSchemaTestHandle) ReserveGraphSchemaManagement(o contract.ObjectRef, id uint64) (contract.GraphSchemaManagementLease, error) {
	l, e := h.ReserveGraphSchemaPreparation(o, id)
	if e != nil {
		return nil, e
	}
	return l.(*graphSchemaTestLease), nil
}
func (l *graphSchemaTestLease) Submit(ctx context.Context, p contract.GraphSchemaPreparation, _ contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
	l.submits.Add(1)
	if p != l.plan {
		return contract.GraphSchemaManagementResult{}, errors.New("wrong original opaque plan")
	}
	if l.submit != nil {
		return l.submit(ctx, p)
	}
	return schemaRuntimeReceipt(p), nil
}
func (l *graphSchemaTestLease) Verify(ctx context.Context) (contract.GraphSchemaManagementResult, error) {
	l.verifies.Add(1)
	if l.verify != nil {
		return l.verify(ctx)
	}
	return schemaRuntimeReceipt(l.plan), nil
}
func schemaRuntimeReceipt(p contract.GraphSchemaPreparation) contract.GraphSchemaManagementResult {
	return contract.GraphSchemaManagementResult{State: "completed", Preview: p.Preview(), ReceiptValid: true, Receipt: contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"original"}}}
}
func TestGraphSchemaManagementOwnedOpaquePlanLateSubmitReceiptAndRevokedVerify(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	handles := []*graphSchemaTestHandle{newGraphSchemaTestHandle(42), newGraphSchemaTestHandle(42)}
	opens := 0
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		h := handles[opens]
		opens++
		return h, nil
	}))
	profile, _ := connections.Defaults(connections.Neo4j)
	profile.ID, profile.Name = 42, "original"
	if _, e := m.ConnectSession(context.Background(), profile, ""); e != nil {
		t.Fatal(e)
	}
	provider, e := m.GraphSchemaManagement(42)
	if e != nil {
		t.Fatal(e)
	}
	o := contract.ObjectRef{ProfileID: 42, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}
	l, e := provider.ReserveGraphSchemaManagement(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	cyclic := contract.GraphValue{Kind: "list", Values: make([]contract.GraphValue, 1)}
	cyclic.Values[0] = cyclic
	if _, e := l.Prepare(before, contract.GraphSchemaDraft{Options: cyclic}); !errors.Is(e, graphschema.ErrBounds) {
		t.Fatal("cyclic value cloned before bounds validation", e)
	}
	p, e := l.Prepare(before, contract.GraphSchemaDraft{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := l.Submit(context.Background(), &graphSchemaTestPlan{preview: p.Preview()}, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("forged plan dispatched", e)
	}
	raw := handles[0].leases[1]
	started, release := make(chan struct{}), make(chan struct{})
	raw.submit = func(_ context.Context, p contract.GraphSchemaPreparation) (contract.GraphSchemaManagementResult, error) {
		close(started)
		<-release
		return schemaRuntimeReceipt(p), nil
	}
	out := make(chan contract.GraphSchemaManagementResult, 1)
	errs := make(chan error, 1)
	go func() {
		r, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{})
		out <- r
		errs <- e
	}()
	<-started
	fresh, e := m.ConnectSession(context.Background(), profile, "")
	if e != nil {
		t.Fatal(e)
	}
	close(release)
	r := <-out
	if e := <-errs; !errors.Is(e, contract.ErrRevoked) || !r.Receipt.Completed {
		t.Fatal("late original receipt erased", e)
	}
	r.Receipt.Columns[0] = "caller changed"
	retained, e := l.Verify(context.Background())
	if !errors.Is(e, contract.ErrRevoked) || retained.Receipt.Columns[0] != "original" || raw.verifies.Load() != 0 {
		t.Fatal("revoked verify lost receipt or reached replacement", e)
	}
	l.Close()
	l.Close()
	retained, _ = l.Verify(context.Background())
	if retained.Receipt.Completed || raw.closes.Load() != 1 || handles[1].closed.Load() != 0 || m.Generation(42) != fresh.Generation() {
		t.Fatal("original cleanup changed replacement or retained cache")
	}
}
func TestGraphSchemaManagementRejectsLateVerificationCatalogKeepsOriginalReceipt(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	handles := []*graphSchemaTestHandle{newGraphSchemaTestHandle(42), newGraphSchemaTestHandle(42)}
	opens := 0
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		h := handles[opens]
		opens++
		return h, nil
	}))
	profile, _ := connections.Defaults(connections.Neo4j)
	profile.ID, profile.Name = 42, "original"
	m.ConnectSession(context.Background(), profile, "")
	provider, _ := m.GraphSchemaManagement(42)
	l, e := provider.ReserveGraphSchemaManagement(contract.ObjectRef{ProfileID: 42, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}, 1)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := l.Read(context.Background())
	p, _ := l.Prepare(before, contract.GraphSchemaDraft{})
	if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); e != nil {
		t.Fatal(e)
	}
	started, release := make(chan struct{}), make(chan struct{})
	raw := handles[0].leases[1]
	raw.verify = func(context.Context) (contract.GraphSchemaManagementResult, error) {
		close(started)
		<-release
		r := schemaRuntimeReceipt(p)
		r.HasVerification, r.Matched = true, true
		r.State = "verified"
		return r, nil
	}
	out := make(chan contract.GraphSchemaManagementResult, 1)
	errs := make(chan error, 1)
	go func() { r, e := l.Verify(context.Background()); out <- r; errs <- e }()
	<-started
	m.ConnectSession(context.Background(), profile, "")
	close(release)
	r := <-out
	e = <-errs
	if !errors.Is(e, contract.ErrRevoked) || r.HasVerification || r.State != "completed" || !r.Receipt.Completed {
		t.Fatal("late catalog published or original receipt erased", e)
	}
}
