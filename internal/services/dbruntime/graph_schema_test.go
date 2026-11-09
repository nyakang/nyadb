package dbruntime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type graphSchemaTestHandle struct {
	*lateGraphHandle
	leases    map[uint64]*graphSchemaTestLease
	closeOnce sync.Once
}

func (h *graphSchemaTestHandle) SupportsGraphSchemaPreparation(o contract.ObjectRef) bool {
	return o.ProfileID == h.id && o.Catalog == "neo4j" && o.Kind == "graph_schema" && o.Name == "graph_schema"
}
func (h *graphSchemaTestHandle) ReserveGraphSchemaPreparation(o contract.ObjectRef, id uint64) (contract.GraphSchemaPreparationLease, error) {
	l := &graphSchemaTestLease{object: o}
	h.leases[id] = l
	return l, nil
}
func (h *graphSchemaTestHandle) Close() error {
	h.closeOnce.Do(func() {
		h.handle.Close()
		for _, l := range h.leases {
			l.Close()
		}
	})
	return nil
}

type graphSchemaTestLease struct {
	object                  contract.ObjectRef
	reads, prepares, closes atomic.Int32
	closeOnce               sync.Once
	started, release        chan struct{}
	last                    *graphSchemaTestObservation
}
type graphSchemaTestObservation struct{ catalog contract.GraphSchemaCatalog }
type graphSchemaTestPlan struct{ preview contract.GraphSchemaPreview }

func (o *graphSchemaTestObservation) Snapshot() contract.GraphSchemaCatalog { return o.catalog }
func (p *graphSchemaTestPlan) Preview() contract.GraphSchemaPreview         { return p.preview }
func (l *graphSchemaTestLease) Object() contract.ObjectRef                  { return l.object }
func (l *graphSchemaTestLease) Generation() uint64                          { return 1 }
func (l *graphSchemaTestLease) Read(context.Context) (contract.GraphSchemaObservation, error) {
	l.reads.Add(1)
	if l.started != nil {
		close(l.started)
		<-l.release
	}
	l.last = &graphSchemaTestObservation{catalog: contract.GraphSchemaCatalog{Object: l.object, Indexes: contract.GraphResult{Columns: []string{"id"}, Rows: [][]contract.GraphValue{{{Kind: "map", Properties: []contract.GraphProperty{{Name: "x", Value: contract.GraphValue{Kind: "string", Text: "original"}}}}}}}}}
	return l.last, nil
}
func (l *graphSchemaTestLease) Prepare(_ contract.GraphSchemaObservation, d contract.GraphSchemaDraft) (contract.GraphSchemaPreparation, error) {
	l.prepares.Add(1)
	return &graphSchemaTestPlan{preview: contract.GraphSchemaPreview{Draft: d, Request: contract.GraphSchemaRequest{Database: l.object.Catalog, Query: "CREATE INDEX `owned`", Parameters: []contract.GraphProperty{{Name: "options", Value: contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "x", Value: contract.GraphValue{Kind: "string", Text: "original"}}}}}}}}}, nil
}
func (l *graphSchemaTestLease) Close() error { l.closeOnce.Do(func() { l.closes.Add(1) }); return nil }
func newGraphSchemaTestHandle(id int64) *graphSchemaTestHandle {
	return &graphSchemaTestHandle{lateGraphHandle: &lateGraphHandle{handle: &handle{id: id, caps: contract.Capabilities{Metadata: true}}}, leases: map[uint64]*graphSchemaTestLease{}}
}

func TestGraphSchemaPreparationPublishedGenerationIndependentEvidenceAndForeignObservations(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var originals []*graphSchemaTestHandle
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := newGraphSchemaTestHandle(p.ID)
		originals = append(originals, h)
		return h, nil
	}))
	p, _ := connections.Defaults(connections.Neo4j)
	p.ID, p.Name = 42, "original"
	// Consume a publication before this profile to prove raw 1 is translated.
	otherProfile := p
	otherProfile.ID = 43
	if _, e := m.ConnectSession(context.Background(), otherProfile, ""); e != nil {
		t.Fatal(e)
	}
	first, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	provider, e := m.GraphSchemaPreparation(42)
	if e != nil {
		t.Fatal(e)
	}
	o := contract.ObjectRef{ProfileID: 42, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}
	foreign := o
	foreign.ProfileID++
	if provider.SupportsGraphSchemaPreparation(foreign) {
		t.Fatal("foreign provider scope accepted")
	}
	if _, e := provider.ReserveGraphSchemaPreparation(foreign, 1); e == nil {
		t.Fatal("foreign reservation accepted")
	}
	l, e := provider.ReserveGraphSchemaPreparation(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	if l.Generation() != first.Generation() || l.Generation() == 1 {
		t.Fatal("private generation published")
	}
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	snapshot := before.Snapshot()
	snapshot.Indexes.Rows[0][0].Properties[0].Value.Text = "changed"
	originals[1].leases[1].last.catalog.Indexes.Rows[0][0].Properties[0].Value.Text = "provider changed"
	if before.Snapshot().Indexes.Rows[0][0].Properties[0].Value.Text != "original" {
		t.Fatal("public observation aliases provider or callers")
	}
	draft := contract.GraphSchemaDraft{Properties: []string{"original"}}
	other, e := provider.ReserveGraphSchemaPreparation(o, 2)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := other.Prepare(before, draft); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("foreign observation reached provider", e)
	}
	if _, e := l.Prepare(&graphSchemaTestObservation{catalog: before.Snapshot()}, draft); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("forged observation reached provider", e)
	}
	plan, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	draft.Properties[0] = "changed"
	copy := plan.Preview()
	copy.Request.Parameters[0].Value.Properties[0].Value.Text = "changed"
	if plan.Preview().Draft.Properties[0] != "original" || plan.Preview().Request.Parameters[0].Value.Properties[0].Value.Text != "original" {
		t.Fatal("public preview shares mutable inputs")
	}
	fresh, e := m.ConnectSession(context.Background(), p, "")
	if e != nil || fresh.Generation() <= first.Generation() {
		t.Fatal(e)
	}
	if provider.SupportsGraphSchemaPreparation(o) {
		t.Fatal("old provider followed replacement")
	}
	if _, e := l.Read(context.Background()); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old read reached replacement", e)
	}
	if _, e := l.Prepare(before, draft); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old preparation reached replacement", e)
	}
	if _, e := provider.ReserveGraphSchemaPreparation(o, 3); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old reservation reached replacement", e)
	}
	if e := l.Close(); e != nil {
		t.Fatal(e)
	}
	l.Close()
	first.Close()
	if originals[1].leases[1].closes.Load() != 1 || originals[1].leases[1].reads.Load() != 1 || originals[1].leases[1].prepares.Load() != 1 || originals[2].closed.Load() != 0 {
		t.Fatal("old cleanup replayed or closed replacement")
	}
	if before.Snapshot().Object != o || plan.Preview().Draft.Properties[0] != "original" {
		t.Fatal("revocation erased original evidence")
	}
}

func TestGraphSchemaPreparationRejectsLateOriginalReadPublication(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	handles := []*graphSchemaTestHandle{newGraphSchemaTestHandle(42), newGraphSchemaTestHandle(42)}
	var opens int
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		h := handles[opens]
		opens++
		return h, nil
	}))
	p, _ := connections.Defaults(connections.Neo4j)
	p.ID, p.Name = 42, "late original"
	first, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	provider, _ := m.GraphSchemaPreparation(42)
	o := contract.ObjectRef{ProfileID: 42, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}
	l, e := provider.ReserveGraphSchemaPreparation(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	raw := handles[0].leases[1]
	raw.started, raw.release = make(chan struct{}), make(chan struct{})
	out := make(chan error, 1)
	go func() {
		observation, e := l.Read(context.Background())
		if observation != nil {
			e = errors.New("late original observation published")
		}
		out <- e
	}()
	<-raw.started
	fresh, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	close(raw.release)
	if e := <-out; !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	l.Close()
	first.Close()
	if m.Generation(42) != fresh.Generation() || handles[1].closed.Load() != 0 || raw.closes.Load() != 1 {
		t.Fatal("late original cleanup reached replacement")
	}
}
