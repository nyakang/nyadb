package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDataSession(t *testing.T, cfgName string) (*Session, contract.ObjectRef, *Client) {
	t.Helper()
	cfg, secret := nativeFixture(t, cfgName)
	s := testSession(t, cfg, secret)
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	db := nativeID()
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db})
	t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + db}) })
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/_security", Body: []byte(`{"admins":{"names":[],"roles":[]},"members":{"names":[],"roles":[]}}`)})
	return s, s.Namespace(db).Object("documents", "_all_docs"), admin
}
func TestCouchDBNativeDataPagingOriginalRowsFieldsAndCompleteExport(t *testing.T) {
	s, o, admin := nativeDataSession(t, "NYADB_COUCHDB_CONFIG")
	const ordinary = 5003
	for start := 0; start < ordinary; start += 200 {
		docs := make([]map[string]any, 0, 200)
		for i := start; i < min(start+200, ordinary); i++ {
			docs = append(docs, map[string]any{"_id": fmt.Sprintf("row_%05d", i), "n": json.Number("9007199254740993"), "null": nil, "nested": []any{false, "", 42}})
		}
		body, _ := json.Marshal(map[string]any{"docs": docs})
		response := nativeRun(t, admin, Request{Method: "POST", Path: "/" + o.Catalog + "/_bulk_docs", Body: body})
		var receipts []struct {
			OK             bool `json:"ok"`
			ID, Rev, Error string
		}
		if decodeJSON(response.Body, &receipts) != nil || len(receipts) != len(docs) {
			t.Fatal("owned bulk setup receipts")
		}
		for _, receipt := range receipts {
			if !receipt.OK || receipt.Error != "" || receipt.Rev == "" {
				t.Fatal("owned bulk setup incomplete")
			}
		}
	}
	large := strings.Repeat("原生🧪", 140000)
	for i := range 12 {
		id := fmt.Sprintf("zlarge_%02d", i)
		body, _ := json.Marshal(map[string]any{"_id": id, "large": large, "n": json.Number("9007199254740993")})
		nativeRun(t, admin, Request{Method: "PUT", Path: "/" + o.Catalog + "/" + id, Body: body})
	}
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + o.Catalog + "/_design/sample", Body: []byte(`{"views":{"v":{"map":"function(doc) { emit(doc._id, doc.n); }"}}}`)})
	lease, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r := lease.Runtime()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	page, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o, Limit: 3})
	if err != nil || len(page.Rows) != 3 || page.Rows[0][0].Text != "_design/sample" || !page.HasMore {
		t.Fatal("native qualified page", err)
	}
	boundary := page.RowRefs[len(page.RowRefs)-1]
	var nativeRef dataIdentity
	if json.Unmarshal(boundary.Values[0], &nativeRef) != nil {
		t.Fatal("original boundary")
	}
	nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + o.Catalog + "/" + url.PathEscape(nativeRef.ID) + "?rev=" + url.QueryEscape(nativeRef.Revision)})
	next, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o, Limit: 3, Offset: 3, After: page.NextCursor})
	if err != nil || len(next.Rows) != 3 || next.Rows[0][0].Text != "row_00002" {
		t.Fatal("native deleted boundary lost successor", err)
	}
	filtered := contract.DataOptions{Object: o, Limit: 2, FilterColumn: "_id", FilterMode: "equal", FilterValue: "row_00000"}
	selected, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, filtered)
	if err != nil || len(selected.Rows) != 1 || selected.Rows[0][0].Text != "row_00000" {
		t.Fatal("native Mango ID filter", err)
	}
	row, err := r.RowReader(ctx, o.ProfileID, o.Catalog, o.Name, *selected.RowRefs[0])
	if err != nil || row.Snapshot.Mode != "values" || !strings.Contains(row.Values["document"].Text, "9007199254740993") {
		t.Fatal("original typed full row", err)
	}
	largeOptions := contract.DataOptions{Object: o, Limit: 20, FilterColumn: "document", FilterMode: "mango", FilterValue: `{"_id":{"$gte":"zlarge_","$lt":"zlarge_z"}}`}
	largePage, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, largeOptions)
	if err != nil || len(largePage.Rows) != 12 || !largePage.Rows[0][2].Truncated {
		t.Fatal("identity paging failed on multiple large documents", err)
	}
	for _, ref := range largePage.RowRefs {
		if len(ref.TypedValues) != 2 || cap(ref.TypedValues) != 2 {
			t.Fatal("native preview identity retains hidden large JSON")
		}
	}
	if _, err = r.RowReader(ctx, o.ProfileID, o.Catalog, o.Name, *largePage.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("complete editing exceeded separate limit", err)
	}
	chunk, err := r.Chunk(ctx, o.ProfileID, o.Catalog, o.Name, "document", *largePage.RowRefs[0], 0, 1024)
	if err != nil || !chunk.HasMore || len([]rune(chunk.Text)) != 1024 {
		t.Fatal("native bounded field", err)
	}
	count, err := r.Count(ctx, o.ProfileID, o.Catalog, o.Name)
	if err != nil || count != ordinary+12 {
		t.Fatal("native unfiltered count range", count, err)
	}
	var output bytes.Buffer
	if err = r.JSONL(ctx, o, &output); err != nil {
		t.Fatal("complete original export beyond 5000", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(output.Bytes(), []byte("\n")), []byte("\n"))
	if len(lines) != ordinary+12 {
		t.Fatal("native complete export capped", len(lines))
	}
	largeSeen := 0
	for _, line := range lines {
		var fields map[string]json.RawMessage
		var id, full string
		if decodeJSON(line, &fields) != nil || json.Unmarshal(fields["_id"], &id) != nil {
			t.Fatal("complete export malformed")
		}
		if id != "_design/sample" && string(fields["n"]) != "9007199254740993" {
			t.Fatal("complete export number changed")
		}
		if strings.HasPrefix(id, "zlarge_") {
			if json.Unmarshal(fields["large"], &full) != nil || full != large {
				t.Fatal("complete export truncated large field")
			}
			largeSeen++
		}
	}
	if largeSeen != 12 {
		t.Fatal("large export documents missing")
	}
	matched := 0
	if err = r.Exporter(ctx, o.ProfileID, o.Catalog, o.Name, filtered, []string{"_id", "document"}, func(values []contract.DataValue) error {
		matched++
		if values[0].Text != "row_00000" || values[1].Kind != contract.JSON || !strings.Contains(values[1].Text, "9007199254740993") {
			t.Error("native filtered export changed range/type")
		}
		return nil
	}); err != nil || matched != 1 {
		t.Fatal("native filtered export", matched, err)
	}
	foreign := *selected.RowRefs[0]
	foreign.Object.Catalog = "different"
	if _, err = r.RowReader(ctx, o.ProfileID, o.Catalog, o.Name, foreign); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign original row accepted", err)
	}
	if _, err = s.ReserveDataDocument(o.Namespace().Object("table", o.Name)); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("relational identity accepted")
	}
	descending := contract.DataOptions{Object: o, Limit: 2, SortColumn: "_id", SortDescending: true}
	desc, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, descending)
	if err != nil || len(desc.Rows) != 2 || desc.Rows[0][0].Text != "zlarge_11" || desc.Rows[1][0].Text != "zlarge_10" {
		t.Fatal("native descending ID order", err)
	}
	descending.After, descending.Offset = desc.NextCursor, 2
	descNext, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, descending)
	if err != nil || len(descNext.Rows) != 2 || descNext.Rows[0][0].Text != "zlarge_09" {
		t.Fatal("native descending boundary", err)
	}
	revision := selected.RowRefs[0].TypedValues[1].Text
	rules := []contract.DataFilterRule{{Column: "_id", Mode: "equal", Value: "row_00000"}, {Column: "_rev", Mode: "equal", Value: revision}, {Column: "_id", Mode: "not_equal", Value: "different"}}
	grouped := contract.DataOptions{Object: o, Limit: 2, Condition: contract.AndFilters(rules)}
	matchedPage, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, grouped)
	if err != nil || len(matchedPage.Rows) != 1 || matchedPage.Rows[0][0].Text != "row_00000" {
		t.Fatal("native grouped ID/revision filters", err)
	}
	body, _ := json.Marshal(map[string]any{"_id": "row_00000", "_rev": revision, "n": json.Number("9007199254740995")})
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + o.Catalog + "/row_00000", Body: body})
	original, err := r.RowReader(ctx, o.ProfileID, o.Catalog, o.Name, *selected.RowRefs[0])
	if err != nil || !strings.Contains(original.Values["document"].Text, "9007199254740993") || strings.Contains(original.Values["document"].Text, "9007199254740995") {
		t.Fatal("original revision fell back to current winner", err)
	}
	current, err := r.AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, filtered)
	if err != nil || len(current.Rows) != 1 || !strings.Contains(current.Rows[0][2].Text, "9007199254740995") {
		t.Fatal("explicit new page did not observe current revision", err)
	}
}

func TestCouchDBNativeDataCanceledOriginalPageKeepsIndependentCountAndLease(t *testing.T) {
	cfg, secret := nativeFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	db := nativeID()
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db})
	t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + db}) })
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/row", Body: []byte(`{"n":9007199254740993}`)})
	entered, acked := make(chan struct{}), make(chan struct{})
	var pages atomic.Int32
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original, err := Open(r.Context(), cfg, secret)
		if err != nil {
			t.Error("native original relay open", err)
			return
		}
		defer original.Close()
		body, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		response, err := original.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		if err != nil {
			t.Error("native relay original read", err)
			return
		}
		if r.URL.Path == "/"+db+"/_all_docs" {
			pages.Add(1)
			close(entered)
			<-r.Context().Done()
			close(acked)
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	originalCfg := cfg
	originalCfg.Endpoint, originalCfg.CAFile, originalCfg.ServerName = relay.URL, ca, relay.Certificate().DNSNames[0]
	s := testSession(t, originalCfg, secret)
	o := s.Namespace(db).Object("documents", "_all_docs")
	lease, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	other, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := lease.Runtime().AdvancedLoader(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o, Limit: 1})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("native original page not captured")
	}
	countCtx, stopCount := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCount()
	if n, err := lease.Runtime().Count(countCtx, o.ProfileID, o.Catalog, o.Name); err != nil || n != 1 {
		t.Fatal("native page blocked independent count", n, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original cancellation lost", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original page cancellation not acknowledged")
	}
	select {
	case <-acked:
	case <-time.After(10 * time.Second):
		t.Fatal("original relay worker did not settle")
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := other.Runtime().Count(countCtx, o.ProfileID, o.Catalog, o.Name); err != nil || n != 1 || pages.Load() != 1 {
		t.Fatal("close/cancel replayed or affected independent lease", strconv.Itoa(int(pages.Load())), err)
	}
}
