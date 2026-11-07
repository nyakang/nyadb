package dataworkspace

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type testDocumentLease struct {
	runtime   Runtime
	cancel    context.CancelFunc
	work      sync.WaitGroup
	closed    atomic.Int32
	resources atomic.Int32
}

func (l *testDocumentLease) Runtime() Runtime { return l.runtime }
func (l *testDocumentLease) Close() error {
	if l.closed.Add(1) == 1 {
		l.cancel()
		l.work.Wait()
		l.resources.Store(0)
	}
	return nil
}

func TestDocumentLeaseOwnsUnpublishedPageAndWaitsOriginalExecutor(t *testing.T) {
	active := Session{ProfileID: 12, Generation: 4, Connected: true}
	var leases []*testDocumentLease
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runtime := Runtime{ReserveDocument: func(o contract.ObjectRef) (contract.DataDocumentLease, error) {
		ctx, cancel := context.WithCancel(context.Background())
		l := &testDocumentLease{cancel: cancel}
		l.runtime.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, _ contract.DataOptions) (*contract.DataPage, error) {
			l.work.Add(1)
			defer l.work.Done()
			l.resources.Add(1)
			close(started)
			<-ctx.Done()
			close(canceled)
			<-ack
			return &contract.DataPage{Object: o, Columns: []string{"document"}}, nil
		}
		leases = append(leases, l)
		return l, nil
	}}
	s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime { return runtime }})
	o := contract.NamespaceRef{ProfileID: 12, Catalog: "original-cluster"}.Object("index", "docs")
	a := s.OpenObject(1, o, contract.Capabilities{Documents: true})
	b := s.OpenObject(2, o, contract.Capabilities{Documents: true})
	if len(leases) != 2 || leases[0] == leases[1] {
		t.Fatal("documents shared a resource lease")
	}
	s.Load(ui.Services{}, a, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	// Switching profiles parks the result; cancel drops the private channel.
	active.ProfileID = 13
	s.Consume(ui.Services{})
	if !a.loading || a.page != nil {
		t.Fatal("read changed parked document")
	}
	go func() {
		<-canceled
		if leases[0].resources.Load() != 1 {
			t.Error("resources released before executor acknowledged cancellation")
		}
		close(ack)
	}()
	s.Close(a)
	if leases[0].closed.Load() != 1 || leases[0].resources.Load() != 0 || a.results != nil || a.page != nil || !a.closed {
		t.Fatal("discarded result retained its original resource")
	}
	if leases[1].closed.Load() != 0 || b.closed {
		t.Fatal("closing one document closed another")
	}
	s.Close(a)
	s.Close(b)
	if leases[0].closed.Load() != 1 || leases[1].closed.Load() != 1 {
		t.Fatal("Close was not idempotent")
	}
}

func TestDocumentLeaseFailureCannotFallBackToParentRuntime(t *testing.T) {
	for _, failure := range []error{nil, contract.ErrRevoked} {
		t.Run("reservation", func(t *testing.T) {
			var reads atomic.Int32
			r := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return nil, failure }, AdvancedLoader: func(context.Context, int64, string, string, contract.DataOptions) (*contract.DataPage, error) {
				reads.Add(1)
				return nil, nil
			}}
			s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }, Runtime: func() Runtime { return r }})
			d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "cluster"}.Object("index", "docs"), contract.Capabilities{})
			if d.err == "" || d.boundRuntime == nil {
				t.Fatal("failed or nil reservation was accepted")
			}
			s.Load(ui.Services{}, d, false)
			if reads.Load() != 0 || d.loading {
				t.Fatal("failed document fell back to unrelated runtime")
			}
			s.Close(d)
		})
	}
}

func TestDocumentLeasePageNavigationKeepsFirstBoundaryAndRefreshClearsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &testDocumentLease{cancel: cancel}
	requests := make(chan contract.DataOptions, 8)
	l.runtime.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		requests <- o
		return &contract.DataPage{Columns: []string{"document"}, Offset: o.Offset, Limit: o.Limit, KeysetAvailable: true, HasMore: true, CurrentCursor: &contract.DataCursor{Offset: o.Offset, Values: [][]byte{[]byte("original-start")}}, NextCursor: &contract.DataCursor{Offset: o.Offset + int64(o.Limit), Values: [][]byte{[]byte("original-next")}}}, nil
	}
	r := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }, Runtime: func() Runtime { return r }})
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "cluster"}.Object("index", "docs"), contract.Capabilities{Keyset: true})
	defer s.Close(d)
	consume := func() {
		t.Helper()
		select {
		case result := <-d.results:
			out := make(chan mysqlDataResult, 1)
			out <- result
			d.results = out
		case <-time.After(time.Second):
			t.Fatal("read stalled")
		}
		s.Consume(ui.Services{})
		if d.page == nil || d.loading || d.err != "" {
			t.Fatal("page not applied", d.err)
		}
	}
	s.Load(ui.Services{}, d, false)
	consume()
	first := <-requests
	if first.After != nil {
		t.Fatal("first page had a foreign boundary")
	}
	s.changeMySQLDataPage(ui.Services{}, d, int64(d.limit))
	consume()
	next := <-requests
	if next.After == nil || string(next.After.Values[0]) != "original-next" {
		t.Fatal("next page lost original boundary")
	}
	s.changeMySQLDataPage(ui.Services{}, d, 0)
	consume()
	back := <-requests
	if back.After == nil || string(back.After.Values[0]) != "original-start" {
		t.Fatal("back to first page created a new snapshot")
	}
	s.Load(ui.Services{}, d, true)
	consume()
	refresh := <-requests
	if refresh.After != nil || refresh.Offset != 0 {
		t.Fatal("explicit refresh reused stale boundary")
	}
	if ctx.Err() != nil {
		t.Fatal("navigation canceled document lease")
	}
}

func TestDocumentInspectorUsesItsCapturedLeaseRuntime(t *testing.T) {
	session := func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &testDocumentLease{cancel: cancel}
	l.runtime = Runtime{
		TableInfo: func(_ context.Context, _ int64, db, name string) (*contract.TableInfo, error) {
			return &contract.TableInfo{Database: db, Name: name, Engine: "original lease"}, nil
		},
		Count: func(context.Context, int64, string, string) (int64, error) { return 17, nil },
		Chunk: func(context.Context, int64, string, string, string, contract.RowRef, int64, int) (*contract.FieldChunk, error) {
			return &contract.FieldChunk{Text: "original source"}, ctx.Err()
		},
	}
	r := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
	s := New(Dependencies{Session: session, Runtime: func() Runtime { return r }, Inspector: InspectorDependencies{Session: session}})
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "cluster"}.Object("index", "docs"), contract.Capabilities{})
	d.page = &contract.DataPage{Columns: []string{"document"}, Rows: [][]contract.DataCell{{{Text: "preview"}}}, RowRefs: []*contract.RowRef{{Object: d.object}}}
	d.selectedRow, d.grid.Cells.Cursor = 0, "document"
	p := s.inspectorPanel(d)
	s.inspector.InfoLoader = func(context.Context, int64, string, string) (*contract.TableInfo, error) {
		t.Error("inspector used parent info loader")
		return nil, nil
	}
	s.inspector.CountLoader = func(context.Context, int64, string, string) (int64, error) {
		t.Error("inspector used parent count loader")
		return 0, nil
	}
	s.inspector.ChunkLoader = func(context.Context, int64, string, string, string, contract.RowRef, int64, int) (*contract.FieldChunk, error) {
		t.Error("inspector used parent field loader")
		return nil, nil
	}
	done := make(chan struct{}, 3)
	invalidate := func() { done <- struct{}{} }
	s.inspector.LoadInfo(p, invalidate, false)
	s.inspector.LoadCount(p, invalidate)
	s.inspector.LoadChunk(p, invalidate)
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("inspector worker did not finish")
		}
	}
	s.Consume(ui.Services{})
	if p.Info() == nil || p.Info().Engine != "original lease" || p.Count() == nil || *p.Count() != 17 || p.ChunkText() != "original source" {
		t.Fatal("inspector lost captured document runtime")
	}
	s.Close(d)
	if p.runtime != nil || p.chunkLoader != nil {
		t.Fatal("closed inspector retained document lease callbacks")
	}
}
