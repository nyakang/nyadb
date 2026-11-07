package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDocumentSession(t *testing.T, ctx context.Context) *Session {
	t.Helper()
	cfg, secret := nativeConfig(t)
	raw, err := OpenSession(ctx, sessionProfile(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func nativeDataLease(t *testing.T, s *Session, kind, name string) *dataLease {
	t.Helper()
	raw, err := s.ReserveDataDocument(s.Namespace("").Object(kind, name))
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*dataLease)
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func nativeNoPITs(t *testing.T, ctx context.Context, c *Client, index string) {
	t.Helper()
	r := nativeRun(t, c, ctx, "GET", "/"+index+"/_stats/search?filter_path=indices.*.total.search.open_contexts", nil)
	var stats struct {
		Indices map[string]struct {
			Total struct {
				Search struct {
					Open *int `json:"open_contexts"`
				} `json:"search"`
			} `json:"total"`
		} `json:"indices"`
	}
	if decodeJSON(r.Body, &stats) != nil || len(stats.Indices) == 0 {
		t.Fatal("native resource statistics unavailable")
	}
	for _, v := range stats.Indices {
		if v.Total.Search.Open == nil || *v.Total.Search.Open != 0 {
			t.Fatal("original document resource remains open")
		}
	}
}
func TestElasticsearchNativeDataDocumentExactRowsPagingFieldsFilteredExportAndCleanup(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	long := strings.Repeat("中文🐱", 900)
	text, _ := json.Marshal(long)
	for i := 0; i < 16; i++ {
		routing := "tenant-a"
		if i%2 == 1 {
			routing = "tenant-b"
		}
		source := json.RawMessage(`{"n":` + strconv.Itoa(i%2) + `,"exact":9007199254740993,"decimal_native":12345678901234567890.123456789,"nested":{"value":true,"items":[1,null,"猫"]},"long":` + string(text) + `}`)
		if _, err := admin.CreateDocument(ctx, DocumentID{Index: index, ID: strconv.Itoa(i), Routing: routing}, source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.CreateDocument(ctx, DocumentID{Index: index, ID: "unrouted"}, json.RawMessage(`{"n":0}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	s := nativeDocumentSession(t, ctx)
	a := nativeDataLease(t, s, "index", index)
	b := nativeDataLease(t, s, "index", index)
	r := a.Runtime()
	o := contract.DataOptions{Object: a.object, Limit: 3}
	db := s.identity.ClusterName
	first, err := r.AdvancedLoader(ctx, s.ProfileID(), db, index, o)
	if err != nil || len(first.Rows) != 3 || !first.KeysetAvailable || first.CurrentCursor == nil || first.NextCursor == nil {
		t.Fatal("native shared data binding failed", err)
	}
	if !slices.Equal(first.Columns, documentFields()) || first.Editable == nil || !*first.Editable {
		t.Fatal("normal index lost its native document editing capability")
	}
	if _, err = b.Runtime().AdvancedLoader(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: b.object, Offset: 3, Limit: 3, After: first.NextCursor}); err == nil {
		t.Fatal("cursor crossed document leases")
	}
	if _, err = b.Runtime().AdvancedLoader(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: b.object, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	ref := first.RowRefs[0]
	full, err := r.RowReader(ctx, s.ProfileID(), db, index, *ref)
	if err != nil || full.Snapshot == nil {
		t.Fatal("full native document unavailable", err)
	}
	if full.Values["_id"].Text != "unrouted" {
		if !strings.Contains(full.Values["document"].Text, "9007199254740993") || !strings.Contains(full.Values["document"].Text, "12345678901234567890.123456789") || !strings.Contains(full.Values["document"].Text, long) {
			t.Fatal("full document lost exact number or Unicode")
		}
		if !first.Rows[0][3].Truncated || len([]rune(first.Rows[0][3].Text)) != 1024 {
			t.Fatal("page preview was unbounded")
		}
		chunk, err := r.Chunk(ctx, s.ProfileID(), db, index, "document", *ref, 1024, 1024)
		if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
			t.Fatal("bounded native field read failed", err)
		}
	}
	if _, err = r.Chunk(ctx, s.ProfileID(), db, index, "document", *ref, 0, 1025); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unbounded field read accepted")
	}
	if _, err = b.Runtime().RowReader(ctx, s.ProfileID(), db, index, *ref); err == nil {
		t.Fatal("row identity crossed documents")
	}
	if count, err := r.Count(ctx, s.ProfileID(), db, index); err != nil || count != 17 {
		t.Fatal("current target count wrong", err)
	}
	removed := first.RowSnapshots[0]
	identity, err := a.ref(db, removed.Ref)
	if err != nil {
		t.Fatal(err)
	}
	document, err := admin.GetDocument(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.DeleteDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.CreateDocument(ctx, DocumentID{Index: index, ID: "late"}, json.RawMessage(`{"n":0}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	nextOptions := o
	nextOptions.Offset = 3
	nextOptions.After = first.NextCursor
	next, err := r.AdvancedLoader(ctx, s.ProfileID(), db, index, nextOptions)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := r.AdvancedLoader(ctx, s.ProfileID(), db, index, nextOptions)
	if err != nil {
		t.Fatal(err)
	}
	for i := range next.Rows {
		if next.Rows[i][1].Text != repeated.Rows[i][1].Text || next.Rows[i][3].Text != repeated.Rows[i][3].Text {
			t.Fatal("original page changed")
		}
	}
	backOptions := o
	backOptions.After = first.CurrentCursor
	back, err := r.AdvancedLoader(ctx, s.ProfileID(), db, index, backOptions)
	if err != nil || back.Rows[0][1].Text != first.Rows[0][1].Text {
		t.Fatal("return to first page followed live index", err)
	}
	tampered := *first.NextCursor
	tampered.Offset = 6
	bad := o
	bad.Offset = 6
	bad.After = &tampered
	if _, err = r.AdvancedLoader(ctx, s.ProfileID(), db, index, bad); err == nil {
		t.Fatal("modified cursor accepted")
	}
	filtered := contract.DataOptions{Object: a.object, Filters: []contract.DataFilterRule{{Column: "_id", Mode: "equal", Value: "late"}}}
	var values [][]contract.DataValue
	err = r.Exporter(ctx, s.ProfileID(), db, index, filtered, []string{"_index", "_id", "document"}, func(row []contract.DataValue) error { values = append(values, row); return nil })
	if err != nil || len(values) != 1 || values[0][1].Text != "late" || values[0][2].Text != `{"n":0}` {
		t.Fatal("native filtered full export failed", err)
	}
	byRoute := contract.DataFilterRule{Column: "_routing", Mode: "equal", Value: "tenant-b"}
	byID := contract.DataFilterRule{Column: "_id", Mode: "equal", Value: "late"}
	grouped := contract.DataOptions{Object: a.object, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &byRoute}, {Op: "rule", Rule: &byID}}}}
	groupRows := 0
	err = r.Exporter(ctx, s.ProfileID(), db, index, grouped, []string{"_id", "_routing"}, func(row []contract.DataValue) error {
		if row[0].Text != "late" && row[1].Text != "tenant-b" {
			t.Error("native OR grouping changed meaning")
		}
		groupRows++
		return nil
	})
	expectedRows := 9
	if identity.ID.Routing == "tenant-b" {
		expectedRows--
	}
	if err != nil || groupRows != expectedRows {
		t.Fatal("native grouped identity filter failed", err, groupRows)
	}
	var jsonl bytes.Buffer
	if err = r.JSONL(ctx, a.object, &jsonl); err != nil || len(bytes.Split(bytes.TrimSpace(jsonl.Bytes()), []byte("\n"))) != 17 || !strings.Contains(jsonl.String(), "12345678901234567890.123456789") {
		t.Fatal("complete JSONL export lost values", err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.AdvancedLoader(ctx, s.ProfileID(), db, index, o); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document revived", err)
	}
	if _, err = b.Runtime().AdvancedLoader(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: b.object, Limit: 2}); err != nil {
		t.Fatal("closing first document closed second", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, index)
}

func TestElasticsearchNativeDataDocumentAliasScopeAndStreamConcreteIdentity(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	alias := index + "_scope"
	for i := 0; i < 4; i++ {
		if _, err := admin.CreateDocument(ctx, DocumentID{Index: index, ID: strconv.Itoa(i), Routing: "tenant-a"}, json.RawMessage(`{"n":`+strconv.Itoa(i%2)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": alias, "filter": map[string]any{"term": map[string]int{"n": 0}}, "search_routing": "tenant-a"}}}})
	nativeRun(t, admin, ctx, "POST", "/_aliases", body)
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "alias", alias)
	r := l.Runtime()
	db := s.identity.ClusterUUID
	page, err := r.AdvancedLoader(ctx, s.ProfileID(), db, alias, contract.DataOptions{Object: l.object, Limit: 3})
	if err != nil || len(page.Rows) != 2 || page.Rows[0][0].Text != index || page.Rows[0][2].Text != "tenant-a" {
		t.Fatal("alias filter/routing or concrete identity lost", err)
	}
	if count, err := r.Count(ctx, s.ProfileID(), db, alias); err != nil || count != 2 {
		t.Fatal("alias count escaped filter", err)
	}
	row, err := r.RowReader(ctx, s.ProfileID(), db, alias, *page.RowRefs[0])
	if err != nil || row.Values["document"].Text != `{"n":0}` {
		t.Fatal("alias complete source read failed", err)
	}
	identity, err := l.ref(db, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	original, err := admin.GetDocument(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ReplaceDocument(ctx, original, json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	if _, err = r.RowReader(ctx, s.ProfileID(), db, alias, *page.RowRefs[0]); err == nil {
		t.Fatal("full row bypassed original alias filter")
	}
	var scoped bytes.Buffer
	if err = r.JSONL(ctx, l.object, &scoped); err != nil || string(bytes.TrimSpace(scoped.Bytes())) != `{"n":0}` {
		t.Fatal("alias export bypassed original filter", err)
	}
	stream, template := index+"_stream", index+"_template"
	nativeRun(t, admin, ctx, "PUT", "/_index_template/"+template, []byte(`{"index_patterns":["`+stream+`"],"data_stream":{},"template":{"settings":{"number_of_shards":1,"number_of_replicas":0}}}`))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + stream})
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
	})
	nativeRun(t, admin, ctx, "PUT", "/_data_stream/"+stream, nil)
	nativeRun(t, admin, ctx, "PUT", "/"+stream+"/_create/one", []byte(`{"@timestamp":"2026-11-08T00:00:00Z","value":9007199254740993}`))
	nativeRun(t, admin, ctx, "POST", "/"+stream+"/_refresh", nil)
	ls := nativeDataLease(t, s, "data_stream", stream)
	p, err := ls.Runtime().AdvancedLoader(ctx, s.ProfileID(), db, stream, contract.DataOptions{Object: ls.object, Limit: 2})
	if err != nil || len(p.Rows) != 1 || !strings.HasPrefix(p.Rows[0][0].Text, ".ds-"+stream) || !strings.Contains(p.Rows[0][3].Text, "9007199254740993") {
		t.Fatal("stream lost original backing index or precise source", err)
	}
	full, err := ls.Runtime().RowReader(ctx, s.ProfileID(), db, stream, *p.RowRefs[0])
	if err != nil || full.Values["_id"].Text != "one" {
		t.Fatal("unrouted backing document read failed", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, index)
	nativeNoPITs(t, ctx, admin, stream)
}

func TestElasticsearchNativeDataDocumentRejectsSameNameIndexRecreation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	id := DocumentID{Index: index, ID: "one"}
	if _, err := admin.CreateDocument(ctx, id, json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	p, err := l.page(ctx, s.ProfileID(), s.identity.ClusterUUID, index, contract.DataOptions{Object: l.object, Limit: 1})
	if err != nil || len(p.RowRefs) != 1 {
		t.Fatal(err)
	}
	originalUUID, err := indexUUID(ctx, admin, index)
	if err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "DELETE", "/"+index, nil)
	nativeRun(t, admin, ctx, "PUT", "/"+index, []byte(`{"settings":{"number_of_replicas":0}}`))
	if _, err := admin.CreateDocument(ctx, id, json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	newUUID, err := indexUUID(ctx, admin, index)
	if err != nil || originalUUID == newUUID {
		t.Fatal("native index recreation did not change identity", err)
	}
	if _, err := l.row(ctx, s.ProfileID(), s.identity.ClusterUUID, index, *p.RowRefs[0]); err == nil {
		t.Fatal("original row silently read replacement index")
	}
	// Deleting the original index invalidates its PIT. Cleanup may report a
	// native missing resource; that is not treated as a successful receipt.
	_ = l.Close()
	if _, err := l.page(ctx, s.ProfileID(), s.identity.ClusterUUID, index, contract.DataOptions{Object: l.object, Limit: 1}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document rebound after index recreation", err)
	}
}
