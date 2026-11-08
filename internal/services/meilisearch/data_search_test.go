package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func searchOptions(l *dataLease, raw string, limit int) contract.DataOptions {
	return contract.DataOptions{Object: l.object, Limit: limit, FilterColumn: "document", FilterMode: "meilisearch_search", FilterValue: raw}
}

func TestMeilisearchSearchOriginalSourcesResponseCursorAndExportScopes(t *testing.T) {
	var searches, sourceReads atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/indexes/owned":
			fmt.Fprint(w, mockIndex)
		case "/indexes/owned/search":
			searches.Add(1)
			var q struct {
				Q             string `json:"q"`
				Offset, Limit int
				Attributes    []string `json:"attributesToRetrieve"`
				Ranking       bool     `json:"showRankingScore"`
			}
			if json.NewDecoder(r.Body).Decode(&q) != nil || q.Q != "cat" || !q.Ranking || len(q.Attributes) != 2 || q.Attributes[0] != "title" || q.Attributes[1] != "id" {
				t.Error("native request options or required original primary key changed")
			}
			hits := []map[string]any{}
			for i := q.Offset; i < min(q.Offset+q.Limit, 257); i++ {
				hits = append(hits, map[string]any{"id": fmt.Sprintf("row_%03d", i), "title": "projected", "_formatted": map[string]string{"title": "<em>cat</em>"}, "_rankingScore": json.Number("0.999")})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"query": q.Q, "hits": hits, "offset": q.Offset, "limit": q.Limit, "estimatedTotalHits": 257, "processingTimeMs": 9, "facetDistribution": map[string]any{"category": map[string]any{"cat": 257}}, "future": json.Number("9007199254740993")})
		case "/indexes/owned/documents":
			fmt.Fprint(w, `{"results":[],"offset":0,"limit":0,"total":1017}`)
		default:
			if strings.HasPrefix(r.URL.Path, "/indexes/owned/documents/row_") {
				sourceReads.Add(1)
				id := strings.TrimPrefix(r.URL.Path, "/indexes/owned/documents/")
				fmt.Fprintf(w, `{"id":%q,"title":"original cat","n":9007199254740993,"memo":%q}`, id, strings.Repeat("猫", 2000))
			} else {
				w.WriteHeader(404)
			}
		}
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	a, b := reserveData(t, s), reserveData(t, s)
	raw := `{"q":"cat","attributesToRetrieve":["title"],"showRankingScore":true}`
	o := searchOptions(a, raw, 2)
	p, err := a.page(context.Background(), s.ProfileID(), "owned", "documents", o)
	if err != nil || len(p.Rows) != 2 || p.Search == nil || p.Search.EstimatedTotalHits != 257 || !strings.Contains(p.Search.RawJSON, "facetDistribution") || !strings.Contains(p.Search.RawJSON, "9007199254740993") {
		t.Fatal("native observed response missing", err)
	}
	if !strings.Contains(p.Rows[0][1].Text, "original cat") || strings.Contains(p.Rows[0][1].Text, "_formatted") || p.RowRefs[0] == nil || !p.Rows[0][1].Truncated {
		t.Fatal("search hit mistaken for original document")
	}
	full, err := a.row(context.Background(), s.ProfileID(), "owned", "documents", *p.RowRefs[0])
	if err != nil || !strings.Contains(full.Values["document"].Text, strings.Repeat("猫", 2000)) || full.Snapshot.Mode != "values" {
		t.Fatal("full original fingerprint binding", err)
	}
	o.Offset, o.After = 2, p.NextCursor
	next, err := a.page(context.Background(), s.ProfileID(), "owned", "documents", o)
	if err != nil || next.Rows[0][0].Text != "row_002" {
		t.Fatal("signed native continuation", err)
	}
	back := searchOptions(a, raw, 2)
	back.After = p.CurrentCursor
	if _, err = a.page(context.Background(), s.ProfileID(), "owned", "documents", back); err != nil {
		t.Fatal("original native back boundary", err)
	}
	before := searches.Load()
	if _, err = b.page(context.Background(), s.ProfileID(), "owned", "documents", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("sibling search cursor accepted", err)
	}
	o.FilterValue = `{"q":"other"}`
	if _, err = a.page(context.Background(), s.ProfileID(), "owned", "documents", o); !errors.Is(err, ErrInvalid) || searches.Load() != before {
		t.Fatal("search cursor reused for another query", err)
	}
	var rows int
	err = a.exportRows(context.Background(), s.ProfileID(), "owned", "documents", searchOptions(a, raw, 0), []string{"document"}, func(v []contract.DataValue) error {
		if !strings.Contains(v[0].Text, strings.Repeat("猫", 2000)) || strings.Contains(v[0].Text, "_rankingScore") {
			t.Error("filtered export emitted transformed previews")
		}
		rows++
		return nil
	})
	if err != nil || rows != 257 {
		t.Fatal("native accessible search export", rows, err)
	}
	if count, err := a.count(context.Background(), s.ProfileID(), "owned", "documents"); err != nil || count != 1017 {
		t.Fatal("estimated search hits replaced document count", count, err)
	}
	if sourceReads.Load() < 257 {
		t.Fatal("filtered export did not read original sources")
	}
}

func TestMeilisearchSearchInvalidSelectorsDoNotDispatch(t *testing.T) {
	s, f := mockData(t, `{"id":"one"}`)
	l := reserveData(t, s)
	before := f.calls.Load()
	for _, raw := range []string{`[]`, `null`, `{"q":"a","q":"b"}`, `{"q":"\ud800"}`, `{"offset":0}`, `{"limit":2}`, `{"page":1}`, `{"hitsPerPage":2}`, `{"attributesToRetrieve":[1]}`} {
		if _, err := l.page(context.Background(), s.ProfileID(), "owned", "documents", searchOptions(l, raw, 1)); err == nil {
			t.Fatal("invalid native search accepted", raw)
		}
	}
	if f.calls.Load() != before {
		t.Fatal("invalid native selector dispatched I/O")
	}
}

func TestMeilisearchSearchRejectsMalformedNativeBoundariesAndHits(t *testing.T) {
	for _, mode := range []string{"missing_total", "wrong_offset", "wrong_limit", "duplicate_id", "missing_id", "huge_total"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/indexes/owned" {
					fmt.Fprint(w, mockIndex)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/search") {
					m := map[string]any{"query": "", "hits": []map[string]any{{"id": "one"}}, "offset": 0, "limit": 2, "estimatedTotalHits": 1, "processingTimeMs": 0}
					switch mode {
					case "missing_total":
						delete(m, "estimatedTotalHits")
					case "wrong_offset":
						m["offset"] = 1
					case "wrong_limit":
						m["limit"] = 1
					case "duplicate_id":
						m["hits"] = []map[string]string{{"id": "one"}, {"id": "one"}}
					case "missing_id":
						m["hits"] = []map[string]string{{"title": "missing"}}
					case "huge_total":
						m["estimatedTotalHits"] = json.Number(strconv.FormatUint(^uint64(0), 10))
					}
					_ = json.NewEncoder(w).Encode(m)
					return
				}
				reads.Add(1)
				fmt.Fprint(w, `{"id":"one"}`)
			})
			s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
			l := reserveData(t, s)
			if _, err := l.page(context.Background(), s.ProfileID(), "owned", "documents", searchOptions(l, `{}`, 2)); err == nil {
				t.Fatal("malformed native search became successful page")
			}
			if mode != "duplicate_id" && reads.Load() != 0 {
				t.Fatal("malformed boundaries dispatched original-source reads")
			}
			if !s.Alive() {
				t.Fatal("protocol observation fabricated new session")
			}
		})
	}
}

func TestMeilisearchSearchBodyOwnsPagingAndPreservesExactOptions(t *testing.T) {
	query := `{"q":"🐱","filter":"n = 9007199254740993","facets":["category"],"attributesToRetrieve":[],"future":9007199254740993}`
	body, err := searchBody(query, "id", 100, 25)
	if err != nil || !bytes.Contains(body, []byte("9007199254740993")) || !bytes.Contains(body, []byte(`"offset":100`)) || !bytes.Contains(body, []byte(`"attributesToRetrieve":["id"]`)) {
		t.Fatal("owned search body changed exact native options", err)
	}
}
