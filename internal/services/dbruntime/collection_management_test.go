package dbruntime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type collectionHandle struct {
	tasksHandle
	documents map[uint64]*collectionLease
}

func (h *collectionHandle) SupportsCollectionManagement(o contract.ObjectRef, action string) bool {
	return o == (contract.ObjectRef{ProfileID: h.id, Kind: "collection_management", Name: "collection_management"}) && action == "collection_update"
}
func (h *collectionHandle) ReserveCollectionManagementDocument(o contract.ObjectRef, id uint64) (contract.CollectionManagementLease, error) {
	l := &collectionLease{object: o}
	h.documents[id] = l
	return l, nil
}

type collectionLease struct {
	object                                     contract.ObjectRef
	reads, prepares, submits, verifies, closed atomic.Int32
	closeOnce                                  sync.Once
}
type collectionObservation struct {
	snapshot contract.CollectionManagementSnapshot
}

func (o *collectionObservation) Snapshot() contract.CollectionManagementSnapshot { return o.snapshot }

type collectionPlan struct {
	request  contract.CollectionManagementRequest
	affected []contract.NamespaceRef
}

func (p *collectionPlan) Action() string                                { return "collection_update" }
func (p *collectionPlan) Preview() contract.CollectionManagementRequest { return p.request }
func (p *collectionPlan) Affected() []contract.NamespaceRef             { return p.affected }
func (l *collectionLease) Object() contract.ObjectRef                   { return l.object }
func (l *collectionLease) Generation() uint64                           { return 1 }
func (l *collectionLease) Read(_ context.Context, targets []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
	l.reads.Add(1)
	return &collectionObservation{contract.CollectionManagementSnapshot{Object: l.object, Targets: targets, Reads: []contract.CollectionManagementRead{{Response: contract.CollectionManagementResponse{Status: 200, Body: []byte(`{"result":{"collections":[]}}`)}}}}}, nil
}
func (l *collectionLease) Prepare(_ contract.CollectionManagementObservation, d contract.CollectionManagementDraft) (contract.CollectionManagementPlan, error) {
	l.prepares.Add(1)
	return &collectionPlan{request: contract.CollectionManagementRequest{Method: "PATCH", Path: "/collections/owned", Body: []byte(d.Configuration)}, affected: []contract.NamespaceRef{d.Collection}}, nil
}
func (l *collectionLease) Submit(context.Context, contract.CollectionManagementPlan) (contract.CollectionManagementResult, error) {
	l.submits.Add(1)
	return contract.CollectionManagementResult{State: "response", Receipt: contract.CollectionManagementReceipt{Action: "collection_update", HasResult: true, Result: false, Response: contract.CollectionManagementResponse{Status: 200, Body: []byte(`{"result":false}`)}}, Snapshot: contract.CollectionManagementSnapshot{Object: l.object}}, nil
}
func (l *collectionLease) Verify(context.Context) (contract.CollectionManagementResult, error) {
	l.verifies.Add(1)
	return contract.CollectionManagementResult{}, errors.New("readback unavailable")
}
func (l *collectionLease) Close() error { l.closeOnce.Do(func() { l.closed.Add(1) }); return nil }

func TestCollectionManagementPublishedOriginalGenerationHandlesReceiptsAndClose(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*collectionHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &collectionHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, documents: map[uint64]*collectionLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(2), ""); err != nil {
		t.Fatal(err)
	}
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.CollectionManagement(1)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: 1, Kind: "collection_management", Name: "collection_management"}
	foreign := o
	foreign.ProfileID = 2
	if p.SupportsCollectionManagement(foreign, "collection_update") {
		t.Fatal("foreign management scope published")
	}
	if _, err = p.ReserveCollectionManagementDocument(foreign, 7); err == nil {
		t.Fatal("foreign reservation published")
	}
	l, err := p.ReserveCollectionManagementDocument(o, 7)
	if err != nil {
		t.Fatal(err)
	}
	if l.Object() != o || l.Generation() != first.Generation() || l.Generation() == handles[1].documents[7].Generation() {
		t.Fatal("raw generation was not translated to original publication")
	}
	ref := contract.NamespaceRef{ProfileID: 1, Catalog: "owned"}
	before, err := l.Read(context.Background(), []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := before.Snapshot()
	snapshot.Targets[0].ProfileID++
	snapshot.Reads[0].Response.Body[0] = 'x'
	if before.Snapshot().Targets[0] != ref || before.Snapshot().Reads[0].Response.Body[0] == 'x' {
		t.Fatal("published snapshot accessor shares original buffers")
	}
	draft := contract.CollectionManagementDraft{Action: "collection_update", Collection: ref, Configuration: `{"metadata":{}}`, TimeoutSeconds: 2}
	if _, err = l.Prepare(&collectionObservation{snapshot: before.Snapshot()}, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("forged public observation reached adapter", err)
	}
	plan, err := l.Prepare(before, draft)
	if err != nil {
		t.Fatal(err)
	}
	preview := plan.Preview()
	preview.Body[0] = 'x'
	affected := plan.Affected()
	affected[0].Catalog = "other"
	if string(plan.Preview().Body) != draft.Configuration || plan.Affected()[0] != ref {
		t.Fatal("published plan accessor exposes original mutable buffers")
	}
	other, err := p.ReserveCollectionManagementDocument(o, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Prepare(before, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("different original lease accepted observation", err)
	}
	if _, err = other.Submit(context.Background(), plan); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("different original lease accepted plan", err)
	}
	r, err := l.Submit(context.Background(), plan)
	if err != nil || !r.Receipt.HasResult || r.Receipt.Result {
		t.Fatal("false original receipt lost", err)
	}
	r.Receipt.Response.Body[0] = 'x'
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal(err)
	}
	if p.SupportsCollectionManagement(o, "collection_update") {
		t.Fatal("original provider followed replacement")
	}
	if _, err = p.ReserveCollectionManagementDocument(o, 9); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original reservation followed replacement", err)
	}
	if _, err = l.Read(context.Background(), []contract.NamespaceRef{ref}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original read followed replacement", err)
	}
	if _, err = l.Prepare(before, draft); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original preparation followed replacement", err)
	}
	for _, operation := range []func() (contract.CollectionManagementResult, error){func() (contract.CollectionManagementResult, error) { return l.Submit(context.Background(), plan) }, func() (contract.CollectionManagementResult, error) { return l.Verify(context.Background()) }} {
		r, err = operation()
		if !errors.Is(err, contract.ErrRevoked) || r.State != "indeterminate" || !r.Receipt.HasResult || r.Receipt.Result || !strings.Contains(string(r.Receipt.Response.Body), `"result":false`) {
			t.Fatal("revocation discarded original false receipt or dispatched", err, r.State)
		}
		r.Receipt.Response.Body[0] = 'x'
	}
	old := handles[1].documents[7]
	if old.reads.Load() != 1 || old.prepares.Load() != 1 || old.submits.Load() != 1 || old.verifies.Load() != 0 {
		t.Fatal("original work replayed after replacement")
	}
	newProvider, err := m.CollectionManagement(1)
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := newProvider.ReserveCollectionManagementDocument(o, 7)
	if err != nil {
		t.Fatal(err)
	}
	var closers sync.WaitGroup
	for range 3 {
		closers.Go(func() {
			if err := l.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	closers.Wait()
	other.Close()
	if old.closed.Load() != 1 || handles[2].documents[7].closed.Load() != 0 || !m.IsConnected(1) || newLease.Generation() != second.Generation() {
		t.Fatal("old close affected replacement or repeated close")
	}
	if _, err = newLease.Read(context.Background(), []contract.NamespaceRef{ref}); err != nil {
		t.Fatal(err)
	}
	newLease.Close()
}
