package solr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeData(t *testing.T, s *Session, kind, name string) *dataLease {
	t.Helper()
	raw, err := s.ReserveDataDocument(s.Namespace("").Object(kind, name))
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*dataLease)
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func TestSolrNativeDataProjectionLivePagingLargeFieldsAndCompleteExport(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DATA_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native standalone data fixture")
	}
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 240*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	fields := []map[string]any{
		{"name": "large_text", "type": "string", "stored": true, "indexed": false, "docValues": false},
		{"name": "dv_hidden", "type": "plong", "stored": false, "indexed": false, "docValues": true, "useDocValuesAsStored": false},
		{"name": "dv_visible", "type": "plong", "stored": false, "indexed": false, "docValues": true, "useDocValuesAsStored": true},
		{"name": "copy_src", "type": "string", "stored": true, "indexed": true},
		{"name": "copy_dest", "type": "string", "stored": true, "indexed": true, "multiValued": true},
	}
	for _, field := range fields {
		raw, _ := json.Marshal(map[string]any{"add-field": field})
		nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/schema?wt=json", Body: raw})
	}
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/schema?wt=json", Body: []byte(`{"add-copy-field":{"source":"copy_src","dest":"copy_dest"}}`)})
	docs := make([]map[string]any, 0, 122)
	for i := 0; i < 121; i++ {
		docs = append(docs, map[string]any{"id": fmt.Sprintf("row_%03d", i), "exact_l": json.Number("9007199254740993"), "text_s": "原生 Solr🐱", "dv_hidden": json.Number("9007199254740993"), "dv_visible": json.Number("9007199254740993"), "copy_src": "submitted"})
	}
	docs[0]["large_text"] = strings.Repeat("🐱", 300000)
	body, _ := json.Marshal(docs)
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/update?commit=true&wt=json", Body: body})
	s := testSession(t, cfg, secret)
	a := nativeData(t, s, "core", "nyadb_data")
	other := nativeData(t, s, "core", "nyadb_data")
	r := a.Runtime()
	options := contract.DataOptions{Object: a.object, Limit: 2}
	first, err := r.AdvancedLoader(ctx, s.ProfileID(), s.catalog, "nyadb_data", options)
	if err != nil || len(first.Rows) != 2 || !first.Rows[0][4].Truncated || first.RowSnapshots[0].Mode != "fingerprints" || !first.HasMore {
		t.Fatal("native first projection page", err)
	}
	var projection map[string]json.RawMessage
	complete := nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/nyadb_data/select?q=id:row_001&wt=json&fl=*,_version_"})
	var receipt struct {
		Response struct{ Docs []map[string]json.RawMessage }
	}
	if decodeJSON(complete.Body, &receipt) != nil || len(receipt.Response.Docs) != 1 {
		t.Fatal("native projection receipt")
	}
	projection = receipt.Response.Docs[0]
	if _, ok := projection["dv_hidden"]; ok {
		t.Fatal("hidden docValues falsely treated as source")
	}
	if string(projection["dv_visible"]) != "9007199254740993" || !bytes.Contains(projection["copy_dest"], []byte("submitted")) {
		t.Fatal("native stored/docValues/copy semantics")
	}
	if _, err = r.RowReader(ctx, s.ProfileID(), s.catalog, "nyadb_data", *first.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("complete row exceeded editing bound", err)
	}
	chunk, err := r.Chunk(ctx, s.ProfileID(), s.catalog, "nyadb_data", "document", *first.RowRefs[0], 0, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore || chunk.TotalUnits < 300000 {
		t.Fatal("native large field chunk", chunk, err)
	}
	row, err := r.RowReader(ctx, s.ProfileID(), s.catalog, "nyadb_data", *first.RowRefs[1])
	if err != nil || row.Snapshot.Mode != "values" || !strings.Contains(row.Values["document"].Text, "9007199254740993") {
		t.Fatal("native complete typed row", err)
	}
	// Inserting behind and ahead of the original native boundary proves this is
	// live cursor paging, not a frozen export or an Elasticsearch PIT.
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/update?commit=true&wt=json", Body: []byte(`[{"id":"row_000a","exact_l":9007199254740993},{"id":"row_001a","exact_l":9007199254740993}]`)})
	options.Offset, options.After = 2, first.NextCursor
	next, err := r.AdvancedLoader(ctx, s.ProfileID(), s.catalog, "nyadb_data", options)
	if err != nil || next.Rows[0][2].Text != "row_001a" {
		t.Fatal("native live cursor boundary", err)
	}
	count, err := r.Count(ctx, s.ProfileID(), s.catalog, "nyadb_data")
	if err != nil || count != 123 {
		t.Fatal("native exact count", count, err)
	}
	var output bytes.Buffer
	if err = r.JSONL(ctx, a.object, &output); err != nil || bytes.Count(output.Bytes(), []byte{'\n'}) != 123 || !bytes.Contains(output.Bytes(), []byte("9007199254740993")) || !bytes.Contains(output.Bytes(), []byte("Projection")) {
		t.Fatal("native complete multi-batch export", err)
	}
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/update?commit=true&wt=json", Body: []byte(`[{"id":"row_001","exact_l":9007199254740995}]`)})
	if _, err = r.RowReader(ctx, s.ProfileID(), s.catalog, "nyadb_data", *first.RowRefs[1]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("native changed original version accepted", err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Runtime().AdvancedLoader(ctx, s.ProfileID(), s.catalog, "nyadb_data", contract.DataOptions{Object: other.object, Limit: 2}); err != nil {
		t.Fatal("native independent document closed", err)
	}
	// Handler defaults are native query scope too. Force a standalone local read
	// even if a configured default would otherwise distribute to another core.
	original := nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/nyadb_data/config?wt=json"})
	var configuration struct {
		Config struct {
			Handlers map[string]json.RawMessage `json:"requestHandler"`
		} `json:"config"`
	}
	if decodeJSON(original.Body, &configuration) != nil {
		t.Fatal("native handler definition")
	}
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/config?wt=json", Body: []byte(`{"update-requesthandler":{"name":"/select","class":"solr.SearchHandler","defaults":{"rows":10,"echoParams":"explicit","shards":"127.0.0.1:1/solr/foreign"}}}`)})
	defer func() {
		restore, _ := json.Marshal(map[string]json.RawMessage{"update-requesthandler": configuration.Config.Handlers["/select"]})
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, e := admin.Do(cleanup, Request{Method: "POST", Path: "/nyadb_data/config?wt=json", Body: restore}); e != nil {
			t.Error("owned handler restoration", e)
		}
	}()
	nativeMetadataReady(t, s, "nyadb_data", "core")
	local := nativeData(t, s, "core", "nyadb_data")
	if count, err := local.Runtime().Count(ctx, s.ProfileID(), s.catalog, "nyadb_data"); err != nil || count != 123 {
		t.Fatal("standalone handler defaults redirected the document", count, err)
	}

	// Native defaults can silently filter a match-all query. A fresh document
	// rejects this unverified handler rather than claiming a complete export.
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_data/config?wt=json", Body: []byte(`{"update-requesthandler":{"name":"/select","class":"solr.SearchHandler","defaults":{"rows":10,"fq":"id:row_000"}}}`)})
	nativeMetadataReady(t, s, "nyadb_data", "core")
	filtered := nativeData(t, s, "core", "nyadb_data")
	if _, err := filtered.Runtime().Count(ctx, s.ProfileID(), s.catalog, "nyadb_data"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("native default filter silently narrowed document count", err)
	}
}
func TestSolrNativeDataLateChangeCancellationAndPartialReceipt(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DATA_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native standalone data fixture")
	}
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 150*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	for _, fault := range []string{"reload", "partial", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			var fired atomic.Bool
			entered, acked := make(chan struct{}), make(chan struct{})
			relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
				if strings.HasSuffix(r.URL.Path, "/nyadb_data/select") && fired.CompareAndSwap(false, true) {
					switch fault {
					case "reload":
						_, err := admin.Do(ctx, Request{Method: "GET", Path: "/admin/cores?action=RELOAD&core=nyadb_data&wt=json"})
						if err != nil {
							t.Error(err)
						}
					case "partial":
						var receipt map[string]any
						if decodeJSON(response.Body, &receipt) != nil {
							t.Error("native receipt")
						}
						receipt["responseHeader"].(map[string]any)["partialResults"] = true
						response.Body, _ = json.Marshal(receipt)
					case "cancel":
						close(entered)
						<-r.Context().Done()
						close(acked)
						return true
					}
				}
				return false
			})
			s := testSession(t, relay.config, "")
			document := nativeData(t, s, "core", "nyadb_data")
			runtime := document.Runtime()
			read := func() error {
				_, err := runtime.AdvancedLoader(ctx, s.ProfileID(), s.catalog, "nyadb_data", contract.DataOptions{Object: document.object, Limit: 2})
				return err
			}
			if fault == "cancel" {
				result := make(chan error, 1)
				go func() { result <- read() }()
				select {
				case <-entered:
				case <-time.After(30 * time.Second):
					t.Fatal("native relay not entered")
				}
				if err := document.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) {
						t.Fatal("native executor did not acknowledge", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("native close unsettled")
				}
				select {
				case <-acked:
				case <-time.After(15 * time.Second):
					t.Fatal("native relay cancellation unsettled")
				}
			} else {
				err := read()
				expected := error(ErrDataTargetChanged)
				if fault == "partial" {
					expected = ErrProtocol
				}
				if !errors.Is(err, expected) {
					t.Fatal("native late or partial result published", err)
				}
			}
			original := testSession(t, cfg, secret)
			sibling := nativeData(t, original, "core", "nyadb_data")
			if _, err := sibling.Runtime().Count(ctx, original.ProfileID(), original.catalog, "nyadb_data"); err != nil {
				t.Fatal("independent native scope closed", err)
			}
		})
	}
}
func TestSolrNativeCloudDataMemberShardDuplicateIdentityAndAliasRevocation(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DATA_IT") != "1" || os.Getenv("NYADB_SOLR_CLOUD_IT") != "1" {
		t.Skip("requires owned native Cloud data fixture")
	}
	cfg, secret := readNativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 240*time.Second)
	defer stop()
	admin, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "nyadb_routed_" + nativeID()
	nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?action=CREATE&name=" + name + "&router.name=implicit&shards=s1,s2&replicationFactor=1&collection.configName=owned_default&wt=json"})
	defer func() {
		_, err := admin.Do(context.Background(), Request{Method: "GET", Path: "/admin/collections?action=DELETE&name=" + name + "&wt=json"})
		if err != nil {
			t.Error("owned native collection cleanup", err)
		}
	}()
	s := testSession(t, cfg, secret)
	nativeMetadataReady(t, s, name, "collection")
	for _, shard := range []string{"s1", "s2"} {
		body, _ := json.Marshal([]map[string]any{{"id": "same", "text_s": shard, "exact_l": json.Number("9007199254740993")}})
		nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/" + name + "/update?commit=true&wt=json&_route_=" + url.QueryEscape(shard), Body: body})
	}
	alias := "nyadb_data_alias_" + nativeID()
	nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?action=CREATEALIAS&name=" + alias + "&collections=" + name + ",nyadb_second&wt=json"})
	defer func() {
		_, err := admin.Do(context.Background(), Request{Method: "GET", Path: "/admin/collections?action=DELETEALIAS&name=" + alias + "&wt=json"})
		if err != nil {
			t.Error(err)
		}
	}()
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_second/update?commit=true&wt=json", Body: []byte(`[{"id":"same","exact_l":9007199254740993}]`)})
	d := nativeData(t, s, "alias", alias)
	runtime := d.Runtime()
	options := contract.DataOptions{Object: d.object, Limit: 1}
	identities := map[string]bool{}
	for {
		page, err := runtime.AdvancedLoader(ctx, s.ProfileID(), s.catalog, alias, options)
		if err != nil {
			t.Fatal("native member/shard traversal", err)
		}
		for i, row := range page.Rows {
			key := row[0].Text + ":" + row[1].Text + ":" + row[2].Text
			if identities[key] {
				t.Fatal("native physical identity merged")
			}
			identities[key] = true
			full, err := runtime.RowReader(ctx, s.ProfileID(), s.catalog, alias, *page.RowRefs[i])
			if err != nil || !strings.Contains(full.Values["document"].Text, "9007199254740993") {
				t.Fatal("native original shard row", err)
			}
		}
		if !page.HasMore {
			break
		}
		options.Offset, options.After = page.NextCursor.Offset, page.NextCursor
	}
	if len(identities) != 3 {
		t.Fatal("duplicate native keys lost", identities)
	}
	var result bytes.Buffer
	if err = runtime.JSONL(ctx, d.object, &result); err != nil || bytes.Count(result.Bytes(), []byte{'\n'}) != 3 {
		t.Fatal("native shard-aware complete export", err)
	}
	nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?action=CREATEALIAS&name=" + alias + "&collections=nyadb_second," + name + "&wt=json"})
	if _, err = runtime.Count(ctx, s.ProfileID(), s.catalog, alias); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("native alias document rebound", err)
	}
}
