package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func seedNativeSearch(t *testing.T, c *Client) string {
	t.Helper()
	uid := nativeIndex(t, c)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"searchableAttributes":["title"],"filterableAttributes":["category","rank"],"sortableAttributes":["rank"]}`)}))
	docs := make([]map[string]any, 1017)
	for i := range docs {
		category, title := "dogs", "dog 原生🐶"
		if i < 205 {
			category, title = "cats", "cat 原生🐱"
		}
		docs[i] = map[string]any{"id": fmt.Sprintf("row_%04d", i), "title": title, "category": category, "rank": i, "n": json.Number("9007199254740993"), "unknown": map[string]any{"null": nil, "array": []any{false, "猫🐱"}}}
	}
	docs[0]["id"] = json.Number("9007199254740993")
	docs[0]["memo"] = strings.Repeat("猫🐱", 1500)
	raw, _ := json.Marshal(docs)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: raw}))
	return uid
}

const nativeCatsSearch = `{"q":"cat","filter":"category = cats","sort":["rank:desc"],"facets":["category"],"attributesToRetrieve":["title"],"attributesToHighlight":["title"],"showRankingScore":true}`

func nativeSearchOptions(object contract.ObjectRef, query string) contract.DataOptions {
	return contract.DataOptions{Object: object, Limit: 100, FilterColumn: "document", FilterMode: "meilisearch_search", FilterValue: query}
}

func TestMeilisearchNativeSearchOriginalSourcesFacetsSortingAndBoundedExport(t *testing.T) {
	s, admin := nativeData(t)
	uid := seedNativeSearch(t, admin)
	object := s.Namespace(uid).Object("documents", "documents")
	lease, err := s.ReserveDataDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r := lease.Runtime()
	o := nativeSearchOptions(object, nativeCatsSearch)
	first, err := r.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil {
		t.Fatal("native search", err)
	}
	if first.Search == nil || first.Search.EstimatedTotalHits != 205 || len(first.Rows) != 100 || first.Rows[0][0].Text != "row_0204" || !first.HasMore {
		t.Fatal("native sorted/filter page")
	}
	response, err := nativeObject([]byte(first.Search.RawJSON))
	if err != nil || !bytes.Contains(response["facetDistribution"], []byte(`"cats":205`)) || !bytes.Contains(response["hits"], []byte("_formatted")) || !bytes.Contains(response["hits"], []byte("_rankingScore")) || bytes.Contains(response["hits"], []byte(`"unknown"`)) {
		t.Fatal("complete projected native search response")
	}
	if !strings.Contains(first.Rows[0][1].Text, "9007199254740993") || strings.Contains(first.Rows[0][1].Text, "_rankingScore") || !strings.Contains(first.Rows[0][1].Text, "unknown") {
		t.Fatal("projected/enriched hit used as original source")
	}
	o.Offset, o.After = 100, first.NextCursor
	second, err := r.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil || second.Rows[0][0].Text != "row_0104" {
		t.Fatal("search continuation", err)
	}
	o.Offset, o.After = 200, second.NextCursor
	last, err := r.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil || len(last.Rows) != 5 || last.HasMore || last.Rows[4][0].Text != "9007199254740993" {
		t.Fatal("native numeric ID or last page", err)
	}
	full, err := r.RowReader(context.Background(), s.ProfileID(), uid, "documents", *last.RowRefs[4])
	if err != nil || !strings.Contains(full.Values["document"].Text, strings.Repeat("猫🐱", 1500)) || full.Snapshot.Mode != "values" {
		t.Fatal("native original full row", err)
	}
	chunk, err := r.Chunk(context.Background(), s.ProfileID(), uid, "documents", "document", *last.RowRefs[4], 1024, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("original search field", err)
	}
	o.Offset, o.After = 0, first.CurrentCursor
	back, err := r.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil || back.Rows[0][0].Text != "row_0204" {
		t.Fatal("signed search back", err)
	}
	o.FilterValue = `{"q":"dog"}`
	if _, err = r.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign search cursor", err)
	}
	sibling, err := s.ReserveDataDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	o.FilterValue = nativeCatsSearch
	if _, err = sibling.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("sibling search cursor", err)
	}
	if count, err := r.Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 1017 {
		t.Fatal("unfiltered document count", count, err)
	}
	var filtered int
	err = r.Exporter(context.Background(), s.ProfileID(), uid, "documents", nativeSearchOptions(object, nativeCatsSearch), []string{"document"}, func(row []contract.DataValue) error {
		filtered++
		if !strings.Contains(row[0].Text, `"category":"cats"`) || strings.Contains(row[0].Text, "_formatted") || !strings.Contains(row[0].Text, "9007199254740993") {
			return ErrProtocol
		}
		return nil
	})
	if err != nil || filtered != 205 {
		t.Fatal("filtered original-source export", filtered, err)
	}
	var complete bytes.Buffer
	if err = r.JSONL(context.Background(), object, &complete); err != nil || len(bytes.Split(bytes.TrimSpace(complete.Bytes()), []byte{'\n'})) != 1017 {
		t.Fatal("complete JSONL ignored native search", err)
	}
	// The server's default maxTotalHits is 1000. Native search retains that
	// bound, while complete JSONL enumerates all 1017 original documents.
	var capped int
	err = r.Exporter(context.Background(), s.ProfileID(), uid, "documents", nativeSearchOptions(object, `{}`), []string{"id"}, func([]contract.DataValue) error { capped++; return nil })
	if err != nil || capped != 1000 {
		t.Fatal("native maxTotalHits", capped, err)
	}
	empty := "empty_" + uid
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + empty + `"}`)}))
	emptyLease, err := s.ReserveDataDocument(s.Namespace(empty).Object("documents", "documents"))
	if err != nil {
		t.Fatal(err)
	}
	defer emptyLease.Close()
	page, err := emptyLease.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), empty, "documents", nativeSearchOptions(s.Namespace(empty).Object("documents", "documents"), `{"attributesToRetrieve":[]}`))
	if err != nil || len(page.Rows) != 0 || page.Search == nil || page.Search.EstimatedTotalHits != 0 {
		t.Fatal("empty index without primary", err)
	}
	t.Log("native projected/highlighted/ranking response and facets retained; grid/fingerprints/fields use original documents; exact numeric IDs, search205/page100/100/5, unfiltered1017, native search1000 bound and fullJSONL1017 verified")
}

func TestMeilisearchNativeSearchErrorsAndOriginalRequestLifecycle(t *testing.T) {
	for _, mode := range []string{"lost_search", "cancel_search", "close_source", "deleted_source", "export_total_change", "export_duplicate"} {
		t.Run(mode, func(t *testing.T) {
			nativeSession, upstream := nativeData(t)
			uid := seedNativeSearch(t, upstream)
			errorObject := nativeSession.Namespace(uid).Object("documents", "documents")
			errorLease, err := nativeSession.ReserveDataDocument(errorObject)
			if err != nil {
				t.Fatal(err)
			}
			defer errorLease.Close()
			for _, invalid := range []string{`{"sort":["unknown:asc"]}`, `{"q":42}`, `{"facets":["unknown"]}`} {
				_, err = errorLease.Runtime().AdvancedLoader(context.Background(), nativeSession.ProfileID(), uid, "documents", nativeSearchOptions(errorObject, invalid))
				var status *HTTPError
				if !errors.As(err, &status) || status.Status != 400 {
					t.Fatal("native error fabricated success", err)
				}
			}
			entered, acked := make(chan struct{}), make(chan struct{})
			var held atomic.Bool
			var attempts atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, request *http.Request, response Response) bool {
				if response.Status != 200 {
					return false
				}
				search := strings.TrimPrefix(request.URL.Path, "/proxy/meili") == "/indexes/"+uid+"/search"
				target := search && (mode == "lost_search" || mode == "cancel_search" || mode == "deleted_source" || strings.HasPrefix(mode, "export_")) || mode == "close_source" && strings.HasSuffix(request.URL.Path, "/documents/row_0204")
				if !target {
					return false
				}
				attempt := attempts.Add(1)
				if strings.HasPrefix(mode, "export_") && attempt != 2 {
					return false
				}
				if !held.CompareAndSwap(false, true) {
					return false
				}
				if mode == "deleted_source" {
					receipt, _, err := upstream.Submit(request.Context(), Request{Method: "DELETE", Path: "/indexes/" + uid + "/documents/row_0204"})
					if err == nil {
						_, err = upstream.WaitTask(request.Context(), receipt, 30*time.Second)
					}
					if err != nil {
						t.Error(err)
					}
					return false
				}
				if mode == "export_total_change" {
					receipt, _, err := upstream.Submit(request.Context(), Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"added","title":"cat","category":"cats","rank":300}]`)})
					if err == nil {
						_, err = upstream.WaitTask(request.Context(), receipt, 30*time.Second)
					}
					if err != nil {
						t.Error(err)
					}
					return false
				}
				if mode == "export_duplicate" {
					// Changing a sort value moves a previously emitted ID into a
					// later live page without changing the estimated hit count.
					receipt, _, err := upstream.Submit(request.Context(), Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"row_0204","title":"cat","category":"cats","rank":-1}]`)})
					if err == nil {
						_, err = upstream.WaitTask(request.Context(), receipt, 30*time.Second)
					}
					if err != nil {
						t.Error(err)
					}
					return false
				}
				close(entered)
				if mode == "lost_search" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
				} else {
					<-request.Context().Done()
				}
				close(acked)
				return true
			})
			_, secret := nativeFixture(t)
			s := testSession(t, relay.config, secret)
			object := s.Namespace(uid).Object("documents", "documents")
			lease, err := s.ReserveDataDocument(object)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			other, err := s.ReserveDataDocument(object)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			directory := t.TempDir()
			t.Setenv("TMPDIR", directory)
			settled := make(chan error, 1)
			var emitted int
			go func() {
				if strings.HasPrefix(mode, "export_") {
					settled <- lease.Runtime().Exporter(ctx, s.ProfileID(), uid, "documents", nativeSearchOptions(object, nativeCatsSearch), []string{"id"}, func([]contract.DataValue) error { emitted++; return nil })
				} else {
					_, err := lease.Runtime().AdvancedLoader(ctx, s.ProfileID(), uid, "documents", nativeSearchOptions(object, nativeCatsSearch))
					settled <- err
				}
			}()
			if mode == "cancel_search" || mode == "close_source" {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("original request did not enter")
				}
				if count, err := other.Runtime().Count(ctx, s.ProfileID(), uid, "documents"); err != nil || count != 1017 {
					t.Fatal("held search killed sibling", err)
				}
				if mode == "cancel_search" {
					cancel()
				} else {
					_ = lease.Close()
				}
			}
			select {
			case err := <-settled:
				if err == nil {
					t.Fatal("incomplete native search returned success")
				}
				if mode == "export_total_change" && !errors.Is(err, contract.ErrDataConflict) || mode == "export_duplicate" && !errors.Is(err, ErrProtocol) {
					t.Fatal("live export conflict missing", err)
				}
				var interrupted *Interrupted
				if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
					t.Fatal("readonly search inferred uncertain write")
				}
			case <-time.After(45 * time.Second):
				t.Fatal("original executor did not settle")
			}
			if !strings.HasPrefix(mode, "export_") && mode != "deleted_source" {
				select {
				case <-acked:
				case <-time.After(5 * time.Second):
					t.Fatal("native executor not acknowledged")
				}
			}
			if emitted != 0 {
				t.Fatal("partial live export escaped private spool")
			}
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 0 {
				t.Fatal("search spool leaked", err)
			}
			if !strings.HasPrefix(mode, "export_") && attempts.Load() != 1 {
				t.Fatal("original native search replayed", attempts.Load())
			}
			if count, err := other.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count < 1016 {
				t.Fatal("healthy original sibling failed", err)
			}
		})
	}
}

func TestMeilisearchNativeSearchScopedPermissionsWithOriginalDocuments(t *testing.T) {
	_, master := nativeData(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("scoped search requires authenticated owned fixture")
	}
	uid := seedNativeSearch(t, master)
	for _, documents := range []bool{false, true} {
		actions := []string{"indexes.get", "search"}
		if documents {
			actions = append(actions, "documents.get")
		}
		body, _ := json.Marshal(map[string]any{"name": "owned-native-search", "actions": actions, "indexes": []string{uid}, "expiresAt": nil})
		response := nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body})
		var key struct{ UID, Key string }
		if json.Unmarshal(response.Body, &key) != nil || key.Key == "" {
			t.Fatal("owned scoped search key unavailable")
		}
		cfg.IndexUID = uid
		s := testSession(t, cfg, key.Key)
		object := s.Namespace(uid).Object("documents", "documents")
		lease, err := s.ReserveDataDocument(object)
		if err != nil {
			t.Fatal(err)
		}
		page, err := lease.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", nativeSearchOptions(object, nativeCatsSearch))
		if documents {
			if err != nil || len(page.Rows) != 100 || !strings.Contains(page.Rows[0][1].Text, "unknown") {
				t.Fatal("scoped original search", err)
			}
		} else {
			var status *HTTPError
			if !errors.As(err, &status) || status.Status != 403 || page != nil {
				t.Fatal("denied original document permission fabricated projected grid", err)
			}
		}
		_ = lease.Close()
		nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + key.UID})
	}
	t.Log("scoped search requires indexes.get/search/documents.get; missing original-source permission is explicit403 without projected-source fallback")
}
