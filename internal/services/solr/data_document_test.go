package solr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataMock struct {
	mu                 sync.Mutex
	documents          map[string]json.RawMessage
	schema, config     string
	boot               atomic.Int64
	core               atomic.Int64
	requests           atomic.Int32
	selects            atomic.Int32
	duplicate, partial bool
	block, acked       chan struct{}
}

func newDataMock() *dataMock {
	m := &dataMock{schema: testSchema, config: testConfig, documents: map[string]json.RawMessage{}}
	m.boot.Store(100)
	m.core.Store(100)
	return m
}
func (m *dataMock) put(id string, version int64, text string) {
	raw, _ := json.Marshal(map[string]any{"id": id, "_version_": version, "exact_l": json.Number("9007199254740993"), "text_s": text})
	m.mu.Lock()
	m.documents[id] = raw
	m.mu.Unlock()
}
func mockMatches(query any, key string) bool {
	if text, ok := query.(string); ok {
		return text == "*:*"
	}
	q := query.(map[string]any)
	if term, ok := q["term"]; ok {
		return term.(map[string]any)["query"] == key
	}
	b := q["bool"].(map[string]any)
	if must, ok := b["must"]; ok {
		if _, ok := must.(string); !ok {
			for _, child := range must.([]any) {
				if !mockMatches(child, key) {
					return false
				}
			}
		}
	}
	if exclude, ok := b["must_not"]; ok && mockMatches(exclude, key) {
		return false
	}
	if should, ok := b["should"]; ok {
		for _, child := range should.([]any) {
			if mockMatches(child, key) {
				return true
			}
		}
		return false
	}
	return true
}
func (m *dataMock) serve(w http.ResponseWriter, r *http.Request) {
	m.requests.Add(1)
	if r.URL.Path == "/solr/admin/info/system" {
		fmt.Fprint(w, nodeJSON(m.boot.Load(), false))
		return
	}
	if r.URL.Path == "/solr/owned/select" {
		m.selects.Add(1)
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		block, acked := m.block, m.acked
		m.mu.Unlock()
		if block != nil {
			close(block)
			<-r.Context().Done()
			close(acked)
			return
		}
		var request map[string]any
		if decodeJSON(raw, &request) != nil {
			w.WriteHeader(400)
			return
		}
		limit, _ := strconv.Atoi(request["limit"].(json.Number).String())
		params := request["params"].(map[string]any)
		_, explicitShards := r.URL.Query()["shards"]
		if params["distrib"] != false || params["shards"] != "" || r.URL.Query().Get("distrib") != "false" || !explicitShards || r.URL.Query().Get("shards") != "" {
			w.WriteHeader(400)
			return
		}
		after, _ := params["cursorMark"].(string)
		if after == "*" {
			after = ""
		}
		m.mu.Lock()
		keys := make([]string, 0, len(m.documents))
		for key := range m.documents {
			if mockMatches(request["query"], key) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		docs := []json.RawMessage{}
		mark, _ := params["cursorMark"].(string)
		for _, key := range keys {
			if key > after && len(docs) < limit {
				docs = append(docs, bytes.Clone(m.documents[key]))
				mark = key
			}
		}
		buckets := []map[string]any{}
		if m.duplicate {
			buckets = append(buckets, map[string]any{"val": "duplicated", "count": 2})
		}
		header := map[string]any{"status": 0}
		if m.partial {
			header["partialResults"] = true
		}
		response := map[string]any{"responseHeader": header, "response": map[string]any{"numFound": len(keys), "numFoundExact": true, "start": 0, "docs": docs}, "facets": map[string]any{"count": len(keys), "nyadb_duplicates": map[string]any{"buckets": buckets}}}
		if mark != "" {
			response["nextCursorMark"] = mark
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.URL.Path {
	case "/solr/admin/cores":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"status":{"owned":`+mockCore("owned", "/owned", time.UnixMilli(m.core.Load()).UTC().Format(time.RFC3339Nano))+`}}`)
	case "/solr/owned/schema":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"schema":`+m.schema+`}`)
	case "/solr/owned/schema/fields":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"fields":[]}`)
	case "/solr/owned/schema/dynamicfields":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"dynamicFields":[]}`)
	case "/solr/owned/config/params":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"response":{"znodeVersion":-1}}`)
	case "/solr/owned/config":
		fmt.Fprint(w, `{"responseHeader":{"status":0},"config":`+m.config+`}`)
	default:
		w.WriteHeader(404)
	}
}
func mockDataDocument(t *testing.T, m *dataMock) (*Session, contract.DataDocumentLease, contract.DataRuntime) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(server.Close)
	s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
	before := m.requests.Load()
	d, err := s.ReserveDataDocument(s.Namespace("").Object("core", "owned"))
	if err != nil || m.requests.Load() != before {
		t.Fatal("reservation performed I/O", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return s, d, d.Runtime()
}
func TestSolrDataLiveBoundariesExactRowsAndIndependentDocuments(t *testing.T) {
	m := newDataMock()
	for _, id := range []string{"a", "c", "e"} {
		m.put(id, 100, "原生🐱")
	}
	s, d, r := mockDataDocument(t, m)
	object := s.Namespace("").Object("core", "owned")
	options := contract.DataOptions{Object: object, Limit: 2}
	first, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", options)
	if err != nil || len(first.Rows) != 2 || !first.HasMore || first.NextCursor == nil || first.CurrentCursor == nil || first.Editable == nil || *first.Editable || first.Rows[0][3].Value.Kind != contract.Integer || !strings.Contains(first.Rows[0][4].Text, "9007199254740993") || first.RowSnapshots[0].Mode != "fingerprints" {
		t.Fatal("first native page", first, err)
	}
	m.put("b", 101, "behind boundary")
	m.put("d", 102, "ahead boundary")
	options.Offset = 2
	options.After = first.NextCursor
	second, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", options)
	if err != nil || len(second.Rows) != 2 || second.Rows[0][2].Text != "d" || second.Rows[1][2].Text != "e" || second.HasMore {
		t.Fatal("live cursor froze or skipped", second, err)
	}
	repeat, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", options)
	if err != nil || repeat.Rows[0][2].Text != "d" {
		t.Fatal("repeat boundary failed", err)
	}
	count, err := r.Count(context.Background(), 71, s.catalog, "owned")
	if err != nil || count != 5 {
		t.Fatal(count, err)
	}
	other, err := s.ReserveDataDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Runtime().AdvancedLoader(context.Background(), 71, s.catalog, "owned", options); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-document cursor accepted", err)
	}
	stale := *first.CurrentCursor
	options.Offset = 0
	options.After = nil
	_, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", options)
	if err != nil {
		t.Fatal(err)
	}
	options.After = &stale
	if _, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", options); !errors.Is(err, ErrInvalid) {
		t.Fatal("prior refresh epoch accepted", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Count(context.Background(), 71, s.catalog, "owned"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document remained usable", err)
	}
	if _, err = other.Runtime().AdvancedLoader(context.Background(), 71, s.catalog, "owned", contract.DataOptions{Object: object, Limit: 2}); err != nil {
		t.Fatal("independent document closed", err)
	}
}
func TestSolrDataOriginalBindingsNoRefreshRebindAndIncompleteReceipts(t *testing.T) {
	for _, change := range []string{"core", "schema", "config", "node", "partial", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			m := newDataMock()
			m.put("a", 100, "original")
			s, _, r := mockDataDocument(t, m)
			o := contract.DataOptions{Object: s.Namespace("").Object("core", "owned"), Limit: 2}
			_, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			switch change {
			case "core":
				m.core.Add(1)
			case "schema":
				m.schema = strings.Replace(m.schema, "plong", "pdouble", 1)
			case "config":
				m.config = strings.Replace(m.config, "SearchHandler", "CustomHandler", 1)
			case "node":
				m.boot.Add(1)
			case "partial":
				m.partial = true
			case "duplicate":
				m.duplicate = true
			}
			m.mu.Unlock()
			_, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
			expected := error(ErrDataTargetChanged)
			if change == "node" {
				expected = contract.ErrRevoked
			}
			if change == "partial" {
				expected = ErrProtocol
			}
			if change == "duplicate" {
				expected = ErrDuplicateKey
			}
			if !errors.Is(err, expected) {
				t.Fatal("changed or incomplete page published", err)
			}
			if change == "core" || change == "schema" || change == "config" {
				m.mu.Lock()
				m.core.Store(100)
				m.schema = testSchema
				m.config = testConfig
				m.mu.Unlock()
				if _, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o); !errors.Is(err, ErrDataTargetChanged) {
					t.Fatal("restored target silently rebound", err)
				}
			}
		})
	}
}
func TestSolrDataTypedRowsChunksCompleteExportsAndScopedFilters(t *testing.T) {
	m := newDataMock()
	m.put("a", 100, strings.Repeat("🐱", 1500))
	m.put("b", 100, "second")
	s, _, r := mockDataDocument(t, m)
	o := contract.DataOptions{Object: s.Namespace("").Object("core", "owned"), Limit: 2}
	first, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
	if err != nil || !first.Rows[0][4].Truncated || len([]rune(first.Rows[0][4].Text)) != 1024 {
		t.Fatal("bounded preview", err)
	}
	row, err := r.RowReader(context.Background(), 71, s.catalog, "owned", *first.RowRefs[0])
	if err != nil || row.Snapshot.Mode != "values" || !strings.Contains(row.Values["document"].Text, "9007199254740993") || len(row.Snapshot.Original) != 5 {
		t.Fatal("complete typed row", row, err)
	}
	chunk, err := r.Chunk(context.Background(), 71, s.catalog, "owned", "document", *first.RowRefs[0], 0, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("bounded field read", chunk, err)
	}
	before := m.requests.Load()
	if _, err = r.Chunk(context.Background(), 71, s.catalog, "owned", "document", *first.RowRefs[0], 0, 1025); !errors.Is(err, contract.ErrUnsupported) || m.requests.Load() != before {
		t.Fatal("oversized chunk dispatched", err)
	}
	var result bytes.Buffer
	if err = r.JSONL(context.Background(), o.Object, &result); err != nil || bytes.Count(result.Bytes(), []byte{'\n'}) != 2 || !bytes.Contains(result.Bytes(), []byte("9007199254740993")) || !bytes.Contains(result.Bytes(), []byte("Projection")) {
		t.Fatal("complete native export", err)
	}
	filter := contract.DataFilterRule{Column: "_key", Mode: "equal", Value: "a"}
	o.Condition = &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &filter}, {Op: "rule", Rule: &contract.DataFilterRule{Column: "_key", Mode: "equal", Value: "missing"}}}}
	filtered, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
	if err != nil || len(filtered.Rows) != 1 {
		t.Fatal("grouped native terms", err)
	}
	m.put("a", 101, "changed")
	if _, err = r.RowReader(context.Background(), 71, s.catalog, "owned", *first.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed original version accepted", err)
	}
}
func TestSolrDataCloseAcknowledgesOwnRequestAndKeepsOtherScope(t *testing.T) {
	m := newDataMock()
	m.put("a", 100, "original")
	s, d, r := mockDataDocument(t, m)
	m.mu.Lock()
	m.block = make(chan struct{})
	m.acked = make(chan struct{})
	entered, acked := m.block, m.acked
	m.mu.Unlock()
	result := make(chan error, 1)
	go func() { _, err := r.Count(context.Background(), 71, s.catalog, "owned"); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never entered")
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Close() }()
	}
	wg.Wait()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original executor not acknowledged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not settle request")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("server request not acknowledged")
	}
	m.mu.Lock()
	m.block = nil
	m.acked = nil
	m.mu.Unlock()
	other, err := s.ReserveDataDocument(s.Namespace("").Object("core", "owned"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if count, err := other.Runtime().Count(context.Background(), 71, s.catalog, "owned"); err != nil || count != 1 {
		t.Fatal("other document closed", err)
	}
}

func TestSolrDataTamperedRowsCursorsRulesAndNativeTypesNeverDispatch(t *testing.T) {
	m := newDataMock()
	m.put("a", 100, "first")
	m.put("b", 100, "second")
	s, _, r := mockDataDocument(t, m)
	o := contract.DataOptions{Object: s.Namespace("").Object("core", "owned"), Limit: 1}
	first, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
	if err != nil {
		t.Fatal(err)
	}
	initial := m.selects.Load()
	for _, change := range []string{"signature", "mark", "offset", "limit", "rules", "foreign_profile"} {
		bad := o
		cursor := *first.NextCursor
		cursor.Values = [][]byte{bytes.Clone(cursor.Values[0])}
		bad.After = &cursor
		bad.Offset = cursor.Offset
		switch change {
		case "signature":
			cursor.RuleSignature = "forged"
		case "mark":
			cursor.Values[0] = []byte(`{"Epoch":1,"Position":{"Unit":0,"Mark":"foreign"}}`)
		case "offset":
			bad.Offset++
		case "limit":
			bad.Limit++
		case "rules":
			bad.FilterColumn, bad.FilterMode, bad.FilterValue = "_key", "equal", "b"
		case "foreign_profile":
			bad.Object.ProfileID++
		}
		if _, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("tampered cursor accepted", change, err)
		}
	}
	ref := *first.RowRefs[0]
	ref.Values = [][]byte{bytes.Clone(ref.Values[0])}
	ref.Values[0][len(ref.Values[0])-2] ^= 1
	if _, err = r.RowReader(context.Background(), 71, s.catalog, "owned", ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered row accepted", err)
	}
	if m.selects.Load() != initial {
		t.Fatal("tampered identity/rules dispatched a data query")
	}
	m.mu.Lock()
	m.documents["a"] = []byte(`{"id":"a","_version_":"100"}`)
	m.mu.Unlock()
	if _, err = r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o); !errors.Is(err, ErrProtocol) {
		t.Fatal("quoted native version accepted", err)
	}
}
func TestSolrDataSessionCloseSettlesOwnLeasesAndLargeChunkKeepsItsBound(t *testing.T) {
	m := newDataMock()
	m.put("a", 100, strings.Repeat("🐱", 300000))
	s, _, r := mockDataDocument(t, m)
	o := contract.DataOptions{Object: s.Namespace("").Object("core", "owned"), Limit: 1}
	first, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RowReader(context.Background(), 71, s.catalog, "owned", *first.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("complete editing bound bypassed", err)
	}
	if chunk, err := r.Chunk(context.Background(), 71, s.catalog, "owned", "document", *first.RowRefs[0], 3000, 1024); err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("large projection cannot be inspected in bounded chunks", err)
	}
	sibling, err := s.ReserveDataDocument(o.Object)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.block = make(chan struct{})
	m.acked = make(chan struct{})
	entered, acked := m.block, m.acked
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := r.Count(context.Background(), 71, s.catalog, "owned"); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("executor never entered")
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not settle original data worker")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not acknowledge own canceled worker")
	}
	if _, err = sibling.Runtime().Count(context.Background(), 71, s.catalog, "owned"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed session kept idle lease alive", err)
	}
}
func TestSolrDataRejectsUnverifiedHandlersAndRetainsNativeShardUnits(t *testing.T) {
	for _, extra := range []string{`"invariants":{"collection":"other"}`, `"appends":{"fq":"hidden:true"}`, `"useParams":"private"`, `"components":["custom"]`, `"defaults":{"fq":"id:a"}`, `"defaults":{"defType":"custom"}`, `"defaults":{"useParams":"private"}`} {
		raw := []byte(`{"requestHandler":{"/select":{"class":"solr.SearchHandler",` + extra + `}}}`)
		if !errors.Is(validateSelectConfig(raw), contract.ErrUnsupported) {
			t.Fatal("unverified handler override accepted", extra)
		}
	}
	if err := validateSelectConfig([]byte(`{"requestHandler":{"/select":{"class":"solr.SearchHandler","defaults":{"rows":10,"shards":"other/core"}}},"initParams":[{"defaults":{"df":"_text_"}}]}`)); err != nil {
		t.Fatal("verified native defaults rejected", err)
	}
	if err := validateSelectConfig([]byte(`{"requestHandler":{"/select":{"class":"solr.SearchHandler"}},"initParams":[{"defaults":{"fq":"id:a"}}]}`)); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("inherited native filter default accepted", err)
	}
	m := newDataMock()
	s, _, r := mockDataDocument(t, m)
	m.mu.Lock()
	m.config = `{"requestHandler":{"/select":{"class":"custom.SearchHandler"}}}`
	m.mu.Unlock()
	if _, err := r.Count(context.Background(), 71, s.catalog, "owned"); !errors.Is(err, contract.ErrUnsupported) || m.selects.Load() != 0 {
		t.Fatal("custom handler dispatched", err)
	}
	state := json.RawMessage(`{"shards":{"old":{"state":"inactive"},"s2":{"state":"active","replicas":{"r2":{"state":"active"}}},"s1":{"state":"active","replicas":{"r1":{"state":"active"}}}}}`)
	target, err := makeTarget("collection", "collection", []string{"collection"}, struct{ State json.RawMessage }{state})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := parseDataBinding(TargetMetadata{Target: target, Members: []MemberMetadata{{Name: "collection", Schema: []byte(testSchema), Config: []byte(testConfig)}}})
	if err != nil || len(binding.Units) != 2 || binding.Units[0].Shard != "s1" || binding.Units[1].Shard != "s2" {
		t.Fatal("native split-parent/shard identity lost", binding, err)
	}
}
