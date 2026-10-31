package sqlworkspace

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type memoryQueries struct {
	mu      sync.Mutex
	records map[string]queries.Record
	before  func(queries.Record)
}

func (r *memoryQueries) List(_ context.Context, kind string, profile int64) ([]queries.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []queries.Record
	for _, v := range r.records {
		if v.Kind == kind && (profile == 0 || v.ProfileID == profile) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *memoryQueries) Put(_ context.Context, v queries.Record) error {
	if r.before != nil {
		r.before(v)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.records[v.ID]; old.Revision >= v.Revision {
		return queries.ErrStaleRevision
	}
	r.records[v.ID] = v
	return nil
}
func (r *memoryQueries) HasRecords(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records) > 0, nil
}
func (r *memoryQueries) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, id)
	return nil
}

type memoryWorkspaceKey struct{ value []byte }

func (k *memoryWorkspaceKey) Get() ([]byte, bool, error) { return k.value, len(k.value) > 0, nil }
func (k *memoryWorkspaceKey) Set(v []byte) error         { k.value = append([]byte(nil), v...); return nil }
func documentStore() (*queries.Store, *memoryQueries) {
	r := &memoryQueries{records: map[string]queries.Record{}}
	return queries.NewStore(r, &memoryWorkspaceKey{}), r
}

func TestNamedSaveAcknowledgesOldSnapshotWithoutClearingNewEdits(t *testing.T) {
	store, repo := documentStore()
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, s)
	d := s.Open(2, "SELECT 1", 7, "db", false)
	d.queryID = "query"
	d.name = "test"
	started, release := make(chan struct{}), make(chan struct{})
	repo.before = func(r queries.Record) {
		if r.Kind == "query" && r.Revision == 1 {
			close(started)
			<-release
		}
	}
	s.Save(d, false, nil)
	awaitSignal(t, started)
	d.Editor.Select(0, d.Editor.Buffer.Len())
	d.Editor.Insert("SELECT 2")
	close(release)
	waitDocumentIO(t, s)
	if !d.Dirty() || d.namedRevision != 1 || s.Named(7)[0].Text != "SELECT 1" {
		t.Fatal("late acknowledgement lost persisted revision or newer draft")
	}
	s.Save(d, false, nil)
	waitDocumentIO(t, s)
	if d.Dirty() || d.namedRevision != 2 {
		t.Fatalf("subsequent save stuck: %s", d.err)
	}
}

func TestWorkspaceFlushSerializesEditsAndCloseDecision(t *testing.T) {
	store, repo := documentStore()
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, s)
	d := s.Open(2, "SELECT 1", 7, "db", false)
	started, release := make(chan struct{}), make(chan struct{})
	repo.before = func(r queries.Record) {
		if r.Kind == "workspace" && r.Revision == 1 {
			close(started)
			<-release
		}
	}
	s.Observe(2, []uint64{2})
	s.saveWorkspace(nil)
	awaitSignal(t, started)
	d.Editor.Insert(" newer")
	s.Observe(2, []uint64{2})
	closed := false
	s.saveWorkspace(func(ui.Services) { closed = true })
	close(release)
	waitDocumentIO(t, s)
	w, e := store.LoadWorkspace(context.Background())
	if e != nil || !closed || w.Revision != 2 || w.Documents[0].Text != d.Editor.Buffer.String() {
		t.Fatalf("stale final workspace: %+v %v closed=%t", w, e, closed)
	}
	// An already flushed dirty draft must still complete the explicit keep action.
	closed = false
	s.saveWorkspace(func(ui.Services) { closed = true })
	waitDocumentIO(t, s)
	if !closed {
		t.Fatal("already saved workspace lost exit continuation")
	}
}

func TestNamedQueryScopeChangeRequiresExplicitRecovery(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "", 1, "a", false)
	lease := &testLease{}
	d.lease = lease
	s.OpenNamed(d, queries.Named{ID: "q", ProfileID: 1, Database: "b", Text: "DELETE FROM t"})
	s.Run(nil, d)
	if !d.NeedsRecovery() || d.RecoverySchema() != "b" || d.profileID != 0 || lease.releases != 1 || lease.executions.Load() != 0 {
		t.Fatal("query executed in the wrong scope")
	}
}

func TestCloseAllRevokesPendingDocumentCallbacks(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(2, "", 1, "a", false)
	release := make(chan struct{})
	done := make(chan struct{}, 1)
	called := false
	s.startIO(d, func(ui.Services) { called = true }, func() ioResult { <-release; done <- struct{}{}; return ioResult{kind: "export"} })
	s.CloseAll()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker blocked")
	}
	s.ConsumeDocuments(ui.Services{})
	if called || len(s.persistence.jobs) > 0 {
		t.Fatal("closed workbench retained callbacks")
	}
}
