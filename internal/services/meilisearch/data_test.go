package meilisearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataFixture struct {
	mu    sync.Mutex
	index string
	docs  []json.RawMessage
	calls atomic.Int32
}

func mockData(t *testing.T, docs ...string) (*Session, *dataFixture) {
	t.Helper()
	fixture := &dataFixture{index: mockIndex}
	for _, doc := range docs {
		fixture.docs = append(fixture.docs, json.RawMessage(doc))
	}
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		fixture.mu.Lock()
		index := fixture.index
		documents := append([]json.RawMessage(nil), fixture.docs...)
		fixture.mu.Unlock()
		switch r.URL.Path {
		case "/indexes/owned":
			fmt.Fprint(w, index)
		case "/indexes/owned/documents":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			start := min(offset, len(documents))
			end := min(start+limit, len(documents))
			rows := documents[start:end]
			if rows == nil {
				rows = []json.RawMessage{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": rows, "offset": offset, "limit": limit, "total": len(documents)})
		default:
			for _, raw := range documents {
				fields, _ := nativeObject(raw)
				id, _ := nativeString(fields["id"])
				if id == "" {
					id = string(fields["id"])
				}
				if r.URL.Path == "/indexes/owned/documents/"+id {
					w.Write(raw)
					return
				}
			}
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code":"document_not_found"}`)
		}
	})
	return testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, ""), fixture
}
func reserveData(t *testing.T, s *Session) *dataLease {
	t.Helper()
	raw, err := s.ReserveDataDocument(s.Namespace("owned").Object("documents", "documents"))
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*dataLease)
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func dataOptions(l *dataLease, limit int) contract.DataOptions {
	return contract.DataOptions{Object: l.object, Limit: limit}
}
func cloneDataValue[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var copy T
	if json.Unmarshal(raw, &copy) != nil {
		t.Fatal("clone")
	}
	return copy
}

func TestMeilisearchDataOriginalPagingFingerprintsValuesAndCursorIsolation(t *testing.T) {
	source := `{"id":9007199254740993,"n":9007199254740993,"future":"原生🐱","memo":"` + strings.Repeat("猫", 2000) + `"}`
	s, _ := mockData(t, source, `{"id":"two","n":2}`, `{"id":-1,"n":3}`)
	l, b := reserveData(t, s), reserveData(t, s)
	if _, err := contract.Require[contract.DataDocumentsProvider](s); err != nil {
		t.Fatal("public document provider unavailable")
	}
	runtime := l.Runtime()
	if !runtime.Capabilities.Documents || !runtime.Capabilities.EditableTables || runtime.Capabilities.Keyset || runtime.Saver == nil || runtime.Verify == nil || !runtime.AsyncWrites || runtime.Importer == nil || !runtime.DocumentImport || !runtime.Capabilities.Import {
		t.Fatal("native asynchronous edit and OFFSET capabilities")
	}
	p, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), "owned", "documents", dataOptions(l, 1))
	if err != nil || len(p.Rows) != 1 || p.Rows[0][0].Value.Kind != contract.Integer || p.Rows[0][0].Text != "9007199254740993" || !p.Rows[0][1].Truncated || p.KeysetAvailable || p.StableOrder || p.PagingMode != "offset" {
		t.Fatal(p, err)
	}
	if len(p.RowSnapshots[0].Fingerprints) != 2 || p.RowSnapshots[0].Mode != "fingerprints" || p.RowSnapshots[0].Ref.TypedValues[0].Text != "9007199254740993" {
		t.Fatal("native fingerprint payload missing")
	}
	ref := *p.RowRefs[0]
	row, err := runtime.RowReader(context.Background(), s.ProfileID(), "owned", "documents", ref)
	if err != nil || row.Snapshot.Mode != "values" || row.Values["document"].Text != source || len(row.Snapshot.Original) != 2 || row.Snapshot.Original[0].Kind != contract.Integer {
		t.Fatal(row, err)
	}
	chunk, err := runtime.Chunk(context.Background(), s.ProfileID(), "owned", "documents", "document", ref, 0, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal(chunk, err)
	}
	count, err := runtime.Count(context.Background(), s.ProfileID(), "owned", "documents")
	if err != nil || count != 3 {
		t.Fatal(count, err)
	}
	o := dataOptions(l, 1)
	o.Offset = 1
	o.After = p.NextCursor
	next, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), "owned", "documents", o)
	if err != nil || next.Rows[0][0].Text != "two" {
		t.Fatal(next, err)
	}
	back := dataOptions(l, 1)
	back.After = p.CurrentCursor
	again, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), "owned", "documents", back)
	if err != nil || again.Rows[0][0].Text != "9007199254740993" {
		t.Fatal(again, err)
	}
	sibling := dataOptions(b, 1)
	sibling.Offset = 1
	sibling.After = p.NextCursor
	if _, err = b.page(context.Background(), s.ProfileID(), "owned", "documents", sibling); !errors.Is(err, ErrInvalid) {
		t.Fatal("sibling cursor accepted", err)
	}
	if _, err = b.row(context.Background(), s.ProfileID(), "owned", "documents", ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("sibling row accepted", err)
	}
	tampered := cloneDataValue(t, ref)
	tampered.TypedValues[0].Kind = contract.Text
	if _, err = l.row(context.Background(), s.ProfileID(), "owned", "documents", tampered); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered typed identity accepted", err)
	}
	changed := cloneDataValue(t, *p.NextCursor)
	changed.RuleSignature = ""
	o.After = &changed
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "documents", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered cursor accepted", err)
	}
	if _, err = l.page(context.Background(), s.ProfileID()+1, "owned", "documents", dataOptions(l, 1)); err == nil {
		t.Fatal("foreign profile accepted")
	}
}
func TestMeilisearchDataChangesBoundsAndUnsupportedSemantics(t *testing.T) {
	s, fixture := mockData(t, `{"id":"one","n":1}`)
	l := reserveData(t, s)
	p, err := l.page(context.Background(), s.ProfileID(), "owned", "documents", dataOptions(l, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []contract.DataOptions{{Object: l.object, Limit: 1, FilterColumn: "id", FilterMode: "equal", FilterValue: "one"}, {Object: l.object, Limit: 1, SortColumn: "id"}, {Object: l.object, Limit: 1, UseKeyset: true}} {
		if _, err = l.page(context.Background(), s.ProfileID(), "owned", "documents", o); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unsupported semantics accepted", err)
		}
	}
	fixture.mu.Lock()
	fixture.docs[0] = json.RawMessage(`{"id":"one","n":2}`)
	fixture.mu.Unlock()
	if _, err = l.row(context.Background(), s.ProfileID(), "owned", "documents", *p.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed original document accepted", err)
	}
	large := `{"id":"one","memo":"` + strings.Repeat("x", 1<<20) + `"}`
	fixture.mu.Lock()
	fixture.docs[0] = json.RawMessage(large)
	fixture.mu.Unlock()
	p, err = l.page(context.Background(), s.ProfileID(), "owned", "documents", dataOptions(l, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.row(context.Background(), s.ProfileID(), "owned", "documents", *p.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("complete row exceeded1MiB", err)
	}
	if _, err = l.chunk(context.Background(), s.ProfileID(), "owned", "documents", "document", *p.RowRefs[0], 0, 1025); !errors.Is(err, ErrBounds) {
		t.Fatal("field preview exceeded1024", err)
	}
	fixture.mu.Lock()
	fixture.index = strings.Replace(mockIndex, "2026-11-08T08:00:00.123456789Z", "2026-11-09T08:00:00.123456789Z", 1)
	fixture.mu.Unlock()
	if _, err = l.count(context.Background(), s.ProfileID(), "owned", "documents"); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("changed original index observation accepted", err)
	}
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "documents", dataOptions(l, 1)); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("changed lease rebound", err)
	}
}
func TestMeilisearchDataExportSpoolsBeforeSuccessAndCleansOriginalFiles(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	s, fixture := mockData(t, `{"id":"one","n":9007199254740993}`, `{"id":-1,"n":9007199254740993}`)
	l := reserveData(t, s)
	var output bytes.Buffer
	if err := l.jsonl(context.Background(), l.object, &output); err != nil || bytes.Count(output.Bytes(), []byte{'\n'}) != 2 || !bytes.Contains(output.Bytes(), []byte("9007199254740993")) {
		t.Fatal(output.String(), err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("owned export files leaked", err)
	}
	fixture.mu.Lock()
	fixture.docs = []json.RawMessage{json.RawMessage(`{"id":"one","n":1}`), json.RawMessage(`{"id":"one","n":2}`)}
	fixture.mu.Unlock()
	var calls int
	err = l.exportRows(context.Background(), s.ProfileID(), "owned", "documents", contract.DataOptions{Object: l.object}, nil, func([]contract.DataValue) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("duplicate traversal emitted partial successful output", err, calls)
	}
	entries, _ = os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatal("failed export files leaked")
	}
}
func TestMeilisearchDataExportLargeHashPartitionsRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-hashes")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < exportHashMemory/sha256.Size+1; i++ {
		hash := sha256.Sum256([]byte(strconv.Itoa(i)))
		if _, err = file.Write(hash[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = validateExportHashes(context.Background(), path, 1); err != nil {
		t.Fatal("bounded partition validation", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = validateExportHashes(ctx, path, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("partition cancellation", err)
	}
}
func TestMeilisearchDataReservationsAndSessionCloseAcknowledgeOriginalWorkers(t *testing.T) {
	var blocked atomic.Bool
	entered, acked := make(chan struct{}), make(chan struct{})
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/indexes/owned" {
			fmt.Fprint(w, mockIndex)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/documents") {
			if blocked.Load() {
				close(entered)
				<-r.Context().Done()
				close(acked)
				return
			}
			fmt.Fprint(w, `{"results":[],"offset":0,"limit":0,"total":0}`)
			return
		}
		w.WriteHeader(404)
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	l := reserveData(t, s)
	for i := 1; i < 32; i++ {
		raw, err := s.ReserveDataDocument(l.object)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
	}
	if _, err := s.ReserveDataDocument(l.object); !errors.Is(err, ErrBounds) {
		t.Fatal("document reservation bound", err)
	}
	blocked.Store(true)
	settled := make(chan error, 1)
	go func() { _, err := l.count(context.Background(), s.ProfileID(), "owned", "documents"); settled <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original count did not enter")
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not await data executor")
	}
	if err := <-settled; err == nil {
		t.Fatal("canceled original count returned success")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("original relay not acknowledged")
	}
	if len(s.documents) != 0 {
		t.Fatal("closed documents not unregistered")
	}
	if _, err := l.count(context.Background(), s.ProfileID(), "owned", "documents"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed lease rebound", err)
	}
}
