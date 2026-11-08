package dataworkspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDocumentLeaseOwnsTabularExportObjectAndParkedCompletion(t *testing.T) {
	active := Session{ProfileID: 12, Generation: 1, Connected: true}
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("documents", "documents")
	_, cancel := context.WithCancel(context.Background())
	lease := &testDocumentLease{cancel: cancel}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	query := `{"q":"猫","filter":"n = 9007199254740993"}`
	lease.runtime = Runtime{Capabilities: contract.Capabilities{Documents: true, Export: true}, Exporter: func(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, emit func([]contract.DataValue) error) error {
		calls.Add(1)
		close(entered)
		<-release
		if id != 12 || db != "original" || name != "documents" || o.Object != object || o.FilterValue != query || o.FilterMode != "meilisearch_search" || len(fields) != 1 || fields[0] != "document" {
			return contract.ErrDataConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit([]contract.DataValue{{Kind: contract.JSON, Text: `{"n":9007199254740993,"text":"原始🐱"}`}})
	}}
	parent := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return lease, nil }, Exporter: func(context.Context, int64, string, string, contract.DataOptions, []string, func([]contract.DataValue) error) error {
		t.Error("document export used parent runtime")
		return contract.ErrUnsupported
	}}
	s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime { return parent }})
	d := s.OpenObject(1, object, lease.runtime.Capabilities)
	defer s.Close(d)
	d.page = &contract.DataPage{Object: object, Columns: []string{"document"}, ColumnKinds: []string{"json"}, Rows: [][]contract.DataCell{{{Text: "preview"}}}}
	d.filterColumn, d.filterMode, d.filterValue = "document", "meilisearch_search", query
	s.openDataExport(d)
	d.transfer.Scope, d.transfer.ExportPath = "all", filepath.Join(t.TempDir(), "original.csv")
	path := d.transfer.ExportPath
	s.startDataExport(ui.Services{}, d)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("bound export did not start")
	}
	active.ProfileID = 13
	d.filterValue = `{"q":"changed after dispatch"}`
	close(release)
	select {
	case result := <-d.transfer.Results:
		out := make(chan mysqlDataTransferResult, 1)
		out <- result
		d.transfer.Results = out
	case <-time.After(time.Second):
		t.Fatal("original export did not settle")
	}
	s.Consume(ui.Services{})
	if !d.transfer.Busy || d.transfer.Completed || active.ProfileID != 13 {
		t.Fatal("parked export rebound active profile")
	}
	active.ProfileID = 12
	s.Consume(ui.Services{})
	raw, err := os.ReadFile(path)
	if err != nil || !d.transfer.Completed || d.transfer.Busy || d.transfer.Err != "" || calls.Load() != 1 || !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), "原始🐱") {
		t.Fatal("original bound export lost values or replayed", err, d.transfer.Err)
	}
}

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
		l.runtime.Capabilities = contract.Capabilities{Documents: true, Keyset: true, Metadata: true, Count: true, FieldChunks: true, EditableTables: true}
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
	for _, keyset := range []bool{true, false} {
		t.Run(map[bool]string{true: "keyset", false: "offset"}[keyset], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l := &testDocumentLease{cancel: cancel}
			l.runtime.Capabilities = contract.Capabilities{Documents: true, Keyset: keyset, Metadata: true, Count: true, FieldChunks: true, EditableTables: true}
			requests := make(chan contract.DataOptions, 8)
			l.runtime.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				requests <- o
				return &contract.DataPage{Columns: []string{"document"}, Offset: o.Offset, Limit: o.Limit, KeysetAvailable: keyset, HasMore: true, CurrentCursor: &contract.DataCursor{Offset: o.Offset, Values: [][]byte{[]byte("original-start")}}, NextCursor: &contract.DataCursor{Offset: o.Offset + int64(o.Limit), Values: [][]byte{[]byte("original-next")}}}, nil
			}
			r := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
			s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }, Runtime: func() Runtime { return r }})
			d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "cluster"}.Object("index", "docs"), contract.Capabilities{Keyset: keyset})
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
			if next.UseKeyset != keyset || next.After == nil || string(next.After.Values[0]) != "original-next" {
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

			if !keyset {
				s.changeMySQLDataPage(ui.Services{}, d, 3*int64(d.limit))
				consume()
				jump := <-requests
				if jump.After != nil || jump.UseKeyset || len(d.cursorHistory) != 1 {
					t.Fatal("new offset traversal retained previous boundaries")
				}
			}
		})
	}
}

func TestDocumentInspectorUsesItsCapturedLeaseRuntime(t *testing.T) {
	session := func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &testDocumentLease{cancel: cancel}
	l.runtime.Capabilities = contract.Capabilities{Documents: true, Keyset: true, Metadata: true, Count: true, FieldChunks: true, EditableTables: true}
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

func TestConfirmedAndVerifiedWritesRefreshOriginalDocumentWithoutOldCursor(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "verified"}[verified], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := make(chan contract.DataOptions, 1)
			l := &testDocumentLease{cancel: cancel}
			l.runtime.Capabilities = contract.Capabilities{Documents: true, Keyset: true, Metadata: true, Count: true, FieldChunks: true, EditableTables: true}
			l.runtime.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
				requests <- o
				return &contract.DataPage{Object: o.Object, Columns: []string{"document"}, Rows: [][]contract.DataCell{{{Text: "fresh snapshot"}}}, Offset: o.Offset, Limit: o.Limit}, ctx.Err()
			}
			runtime := Runtime{ReserveDocument: func(contract.ObjectRef) (contract.DataDocumentLease, error) { return l, nil }}
			s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Generation: 1, Connected: true} }, Runtime: func() Runtime { return runtime }})
			d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("index", "docs"), contract.Capabilities{Documents: true, EditableTables: true, Keyset: true})
			defer s.Close(d)
			snap := &contract.RowSnapshot{Mode: "fingerprints", Ref: contract.RowRef{Object: d.object, Database: d.database, Table: d.table}}
			d.page = &contract.DataPage{Columns: []string{"document"}, ColumnMetadata: []contract.StructureColumn{{Name: "document", ValueKind: contract.JSON}}, Rows: [][]contract.DataCell{{{Text: "old snapshot"}}}, RowRefs: []*contract.RowRef{&snap.Ref}, RowSnapshots: []*contract.RowSnapshot{snap}}
			d.offset = 100
			d.after = &contract.DataCursor{Offset: 100, Values: [][]byte{[]byte("old PIT")}}
			d.cursorHistory = map[int64]*contract.DataCursor{100: d.after}
			d.edit.Drafts = map[string]*mysqlRowDraft{"row": {Row: 0, Kind: "update", Original: snap, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"n":9007199254740993}`}}}}
			result := &contract.DataEditResult{CommitConfirmed: true, RefreshRequired: true, Applied: 1, Outcomes: []contract.DataEditOutcome{{Kind: "update", Original: snap, Saved: snap, PreviewColumns: []string{"document"}, Previews: []contract.DataCell{{Text: "saved source"}}}}}
			d.edit.Busy, d.edit.Request = true, 1
			out := make(chan mysqlEditResult, 1)
			d.edit.Results = out
			message := mysqlEditResult{Generation: 1, Request: 1, Profile: 12, Keys: []string{"row"}, Saved: result}
			if verified {
				d.edit.Uncertain = cloneEditResult(result)
				d.edit.UncertainKeys = []string{"row"}
				message.Saved = nil
				message.Verification = &contract.DataVerification{Status: "applied", Recovered: result}
			}
			out <- message
			s.Consume(ui.Services{})
			select {
			case o := <-requests:
				if o.Object != d.object || o.Offset != 0 || o.After != nil {
					t.Fatal("refresh changed identity or reused old cursor")
				}
			case <-time.After(time.Second):
				t.Fatal("write did not request new read view")
			}
			if d.HasDraft() || d.edit.Uncertain != nil || !d.loading || d.offset != 0 || d.after != nil || d.cursorHistory != nil {
				t.Fatal("write lost settlement or retained old paging view")
			}
			select {
			case page := <-d.results:
				ready := make(chan mysqlDataResult, 1)
				ready <- page
				d.results = ready
			case <-time.After(time.Second):
				t.Fatal("new view did not settle")
			}
			s.Consume(ui.Services{})
			if d.page == nil || d.page.Rows[0][0].Text != "fresh snapshot" || d.loading {
				t.Fatal("new snapshot was not consumed")
			}
		})
	}
}
