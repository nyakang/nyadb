package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type documentFixture struct {
	session            *Session
	object             contract.ObjectRef
	mu                 sync.Mutex
	docs               map[string]string
	requests           atomic.Int32
	blocked            atomic.Bool
	entered, acked     chan struct{}
	enterOnce, ackOnce sync.Once
}

func dataFixture(t *testing.T, total int) *documentFixture {
	t.Helper()
	f := &documentFixture{docs: map[string]string{}, entered: make(chan struct{}), acked: make(chan struct{})}
	for i := range total {
		id := fmt.Sprintf("row_%05d", i)
		f.docs[id] = documentWire(id)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Path == "/" {
			fmt.Fprint(w, mockIdentity)
			return
		}
		if r.URL.Path == "/_session" {
			fmt.Fprint(w, mockAnonymous)
			return
		}
		if r.Method != "GET" && !strings.HasSuffix(r.URL.Path, "/_find") {
			t.Error("read-only fixture received a mutation")
		}
		f.mu.Lock()
		ids := make([]string, 0, len(f.docs))
		for id := range f.docs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		f.mu.Unlock()
		if r.URL.Path == "/owned" {
			fmt.Fprintf(w, `{"db_name":"owned","doc_count":%d,"instance_start_time":"original-observation"}`, len(ids))
			return
		}
		if r.URL.Path == "/owned/_all_docs" {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			descending := r.URL.Query().Get("descending") == "true"
			if descending {
				slices.Reverse(ids)
			}
			var start string
			if r.URL.Query().Get("startkey") != "" {
				if json.Unmarshal([]byte(r.URL.Query().Get("startkey")), &start) != nil {
					t.Error("invalid native start key")
				}
			}
			rows := make([]json.RawMessage, 0, limit)
			for _, id := range ids {
				if start != "" && (!descending && id < start || descending && id > start) {
					continue
				}
				raw, _ := json.Marshal(map[string]any{"id": id, "key": id, "value": map[string]string{"rev": documentTestRevision}})
				rows = append(rows, raw)
				if len(rows) == limit {
					break
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_rows": len(ids), "offset": 0, "update_seq": "opaque", "rows": rows})
			return
		}
		if r.URL.Path == "/owned/_find" {
			_ = json.NewEncoder(w).Encode(map[string]any{"docs": []map[string]string{{"_id": "row_00001", "_rev": documentTestRevision}}, "bookmark": "opaque-next"})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/owned/")
		if f.blocked.Load() {
			f.enterOnce.Do(func() { close(f.entered) })
			<-r.Context().Done()
			f.ackOnce.Do(func() { close(f.acked) })
			return
		}
		if r.URL.Query().Get("rev") != documentTestRevision {
			t.Error("original revision omitted")
		}
		f.mu.Lock()
		body, ok := f.docs[id]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"not_found"}`)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	f.session = testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	f.object = f.session.Namespace("owned").Object("documents", "_all_docs")
	return f
}

func TestCouchDBDataReservationHasNoIOAndPublishesOnlyReadonlyData(t *testing.T) {
	f := dataFixture(t, 2)
	before := f.requests.Load()
	leases := make([]contract.DataDocumentLease, 0, 32)
	for range 32 {
		lease, err := f.session.ReserveDataDocument(f.object)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	if f.requests.Load() != before {
		t.Fatal("reservation performed I/O")
	}
	if _, err := f.session.ReserveDataDocument(f.object); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded reservation", err)
	}
	if _, err := contract.Require[contract.DataDocumentsProvider](f.session); err != nil {
		t.Fatal("verified public provider absent")
	}
	r := leases[0].Runtime()
	if !r.Capabilities.Documents || r.Capabilities.EditableTables || r.Saver != nil || r.Importer != nil {
		t.Fatal("private read capabilities inflated")
	}
	if r.Selector.Dialect != "mango" || r.Selector.Validate == nil || r.ReadDescription == nil {
		t.Fatal("native selector/description feature missing")
	}
	for _, raw := range []string{`[]`, `null`, `{"n":1,"n":2}`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		if r.Selector.Validate(raw) == nil || f.requests.Load() != before {
			t.Fatal("invalid selector reached transport")
		}
	}
	if r.Selector.Validate(`{"n":{"$eq":9007199254740993}}`) != nil || f.requests.Load() != before {
		t.Fatal("pure selector validation changed exact JSON or did I/O")
	}
	if documentReadDescription("owned/child", contract.DataOptions{}) != "GET /owned%2Fchild/_all_docs" {
		t.Fatal("native description changed database path identity")
	}
	for _, lease := range leases {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := f.session.ReserveDataDocument(f.object)
	if err != nil {
		t.Fatal("slot not reclaimed", err)
	}
	defer lease.Close()
}

func TestCouchDBDataRejectsCyclicDeepAndOversizedFiltersBeforeCopyOrIO(t *testing.T) {
	f := dataFixture(t, 1)
	lease, err := f.session.ReserveDataDocument(f.object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r := lease.Runtime()
	cycle := &contract.FilterExpression[contract.DataFilterRule]{Op: "and"}
	cycle.Children = make([]contract.FilterExpression[contract.DataFilterRule], 1)
	cycle.Children[0] = *cycle
	rule := contract.DataFilterRule{Column: "_id", Mode: "equal", Value: "row_00000"}
	deep := &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}
	for range 1000 {
		deep = &contract.FilterExpression[contract.DataFilterRule]{Op: "and", Children: []contract.FilterExpression[contract.DataFilterRule]{*deep}}
	}
	for name, options := range map[string]contract.DataOptions{
		"cyclic":       {Condition: cycle},
		"deep":         {Condition: deep},
		"many_filters": {Filters: make([]contract.DataFilterRule, contract.DataMaxFilterRules+1)},
		"many_sorts":   {Sorts: make([]contract.DataSortRule, 1000)},
	} {
		t.Run(name, func(t *testing.T) {
			options.Object, options.Limit = f.object, 1
			before := f.requests.Load()
			if _, err := r.AdvancedLoader(context.Background(), f.object.ProfileID, "owned", "_all_docs", options); err == nil {
				t.Fatal("malformed page accepted")
			}
			if err := r.Exporter(context.Background(), f.object.ProfileID, "owned", "_all_docs", options, nil, func([]contract.DataValue) error {
				t.Fatal("malformed export invoked sink")
				return nil
			}); err == nil {
				t.Fatal("malformed export accepted")
			}
			if f.requests.Load() != before {
				t.Fatal("malformed expression reached transport")
			}
		})
	}
}

func TestCouchDBDataCursorOriginalRevisionAndTypedSnapshotSafety(t *testing.T) {
	f := dataFixture(t, 4)
	lease, err := f.session.ReserveDataDocument(f.object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r := lease.Runtime()
	o := contract.DataOptions{Object: f.object, Limit: 2}
	page, err := r.AdvancedLoader(context.Background(), f.object.ProfileID, "owned", "_all_docs", o)
	if err != nil || len(page.Rows) != 2 || !page.HasMore || page.Editable == nil || *page.Editable || !page.LiveRead || page.CurrentCursor == nil || page.NextCursor == nil {
		t.Fatal("native page", err)
	}
	snapshot := page.RowSnapshots[0]
	row, err := r.RowReader(context.Background(), f.object.ProfileID, "owned", "_all_docs", snapshot.Ref)
	if err != nil || row.Snapshot.Mode != "values" || row.Values["document"].Kind != contract.JSON || !strings.Contains(row.Values["document"].Text, "9007199254740993") {
		t.Fatal("typed full row", err)
	}
	bad := *page.NextCursor
	bad.Values = [][]byte{bytes.Clone(bad.Values[0])}
	bad.Values[0][0] ^= 1
	o.After = &bad
	o.Offset = 2
	before := f.requests.Load()
	if _, err = r.AdvancedLoader(context.Background(), f.object.ProfileID, "owned", "_all_docs", o); !errors.Is(err, ErrInvalid) || f.requests.Load() != before {
		t.Fatal("forged cursor reached data transport", err)
	}
	badRef := snapshot.Ref
	badRef.TypedValues = slices.Clone(badRef.TypedValues)
	badRef.TypedValues[0].Text = "other"
	if _, err = r.RowReader(context.Background(), f.object.ProfileID, "owned", "_all_docs", badRef); !errors.Is(err, ErrInvalid) || f.requests.Load() != before {
		t.Fatal("forged typed identity reached transport", err)
	}
	f.mu.Lock()
	delete(f.docs, "row_00001")
	f.mu.Unlock()
	o.After = page.NextCursor
	next, err := r.AdvancedLoader(context.Background(), f.object.ProfileID, "owned", "_all_docs", o)
	if err != nil || len(next.Rows) != 2 || next.Rows[0][0].Text != "row_00002" {
		t.Fatal("deleted boundary skipped successor", err)
	}
	f.mu.Lock()
	f.docs["row_00000"] = strings.Replace(documentWire("row_00000"), "9007199254740993", "9007199254740995", 1)
	f.mu.Unlock()
	if _, err = r.RowReader(context.Background(), f.object.ProfileID, "owned", "_all_docs", snapshot.Ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed same-revision body accepted", err)
	}
}

func TestCouchDBDataFieldAndExportKeepCompleteLargeNativeJSON(t *testing.T) {
	f := dataFixture(t, 1)
	large := strings.Repeat("原生🧪", 140000)
	f.mu.Lock()
	f.docs["row_00000"] = fmt.Sprintf(`{"_id":"row_00000","_rev":%q,"large":%q,"n":9007199254740993}`, documentTestRevision, large)
	f.mu.Unlock()
	lease, err := f.session.ReserveDataDocument(f.object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r := lease.Runtime()
	page, err := r.AdvancedLoader(context.Background(), f.object.ProfileID, "owned", "_all_docs", contract.DataOptions{Object: f.object, Limit: 1})
	if err != nil || !page.Rows[0][2].Truncated {
		t.Fatal("bounded large preview", err)
	}
	if identity := page.RowRefs[0].TypedValues; len(identity) != 2 || cap(identity) != 2 {
		t.Fatal("bounded row identity retains hidden full document cells")
	}
	if _, err = r.RowReader(context.Background(), f.object.ProfileID, "owned", "_all_docs", *page.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded full editing payload", err)
	}
	chunk, err := r.Chunk(context.Background(), f.object.ProfileID, "owned", "_all_docs", "document", *page.RowRefs[0], 0, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("bounded native field", err)
	}
	var output bytes.Buffer
	if err = r.JSONL(context.Background(), f.object, &output); err != nil {
		t.Fatal("complete large JSONL", err)
	}
	var doc map[string]json.RawMessage
	var text string
	if decodeJSON(bytes.TrimSpace(output.Bytes()), &doc) != nil || json.Unmarshal(doc["large"], &text) != nil || text != large || string(doc["n"]) != "9007199254740993" {
		t.Fatal("full native export changed data")
	}
}

func TestCouchDBDataCancelKeepsCountIndependentAndCloseSettlesOriginalWorker(t *testing.T) {
	f := dataFixture(t, 2)
	lease, _ := f.session.ReserveDataDocument(f.object)
	defer lease.Close()
	other, _ := f.session.ReserveDataDocument(f.object)
	defer other.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.blocked.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := lease.Runtime().AdvancedLoader(ctx, f.object.ProfileID, "owned", "_all_docs", contract.DataOptions{Object: f.object, Limit: 1})
		done <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original native worker did not enter")
	}
	if n, err := lease.Runtime().Count(context.Background(), f.object.ProfileID, "owned", "_all_docs"); err != nil || n != 2 {
		t.Fatal("page blocked independent count", err)
	}
	closed := make(chan struct{})
	go func() { _ = lease.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not settle original worker")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original cancellation lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker not settled")
	}
	select {
	case <-f.acked:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP cancellation not acknowledged")
	}
	if n, err := other.Runtime().Count(context.Background(), f.object.ProfileID, "owned", "_all_docs"); err != nil || n != 2 {
		t.Fatal("close affected independent lease", err)
	}
}
