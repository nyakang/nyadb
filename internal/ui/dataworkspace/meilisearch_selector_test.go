package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMeilisearchSelectorDispatchResponsePreviewHistoryAndClose(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan contract.DataOptions, 4)
	response := `{"hits":[{"id":"one","future":9007199254740993}],"memo":"` + strings.Repeat("猫", 25000) + `"}`
	r := Runtime{Capabilities: contract.Capabilities{Documents: true}, SortColumns: []string{}, Selector: contract.DataSelector{Dialect: "meilisearch", Column: "document", Mode: "meilisearch_search", Validate: func(raw string) error {
		if !json.Valid([]byte(raw)) || !strings.HasPrefix(raw, "{") {
			return errors.New("invalid native query")
		}
		return nil
	}}, ReadDescription: func(contract.DataOptions) string { return "POST /indexes/owned/search" }, AdvancedLoader: func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
		calls.Add(1)
		requests <- o
		return &contract.DataPage{Object: o.Object, Columns: []string{"id", "document"}, ColumnKinds: []string{"json", "json"}, Rows: [][]contract.DataCell{{{Text: "one"}, {Text: `{"id":"one","n":9007199254740993}`}}}, Search: &contract.DataSearchResult{RawJSON: response, EstimatedTotalHits: 1, ProcessingTimeMS: 3}, Offset: o.Offset, Limit: o.Limit, QuerySQL: "POST /indexes/owned/search"}, nil
	}}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 7, Generation: 1, Connected: true} }, Runtime: func() Runtime { return r }})
	d := s.OpenObject(1, (contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}).Object("documents", "documents"), r.Capabilities)
	defer s.Close(d)
	d.page = &contract.DataPage{Columns: []string{"document"}, Rows: [][]contract.DataCell{{{Text: "cached"}}}}
	old := d.page
	d.selectorDraft = "["
	s.applyDataSelector(ui.Services{}, d)
	if d.selectorError == "" || d.page != old || calls.Load() != 0 {
		t.Fatal("invalid native selector changed cached data")
	}
	query := `{"q":"猫","filter":"n = 9007199254740993","sort":["n:desc"],"facets":["category"]}`
	d.selectorDraft = query
	d.sortRules = []contract.DataSortRule{{Column: "legacy"}}
	s.applyDataSelector(ui.Services{}, d)
	d.selectorDraft = "modified after dispatch"
	wait := func() {
		t.Helper()
		end := time.Now().Add(time.Second)
		for d.loading && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.loading || d.err != "" {
			t.Fatal("native selector did not settle", d.err)
		}
	}
	wait()
	o := <-requests
	if o.FilterValue != query || o.FilterMode != "meilisearch_search" || o.FilterColumn != "document" || len(o.Sorts) != 0 || o.Condition != nil {
		t.Fatal("native query mixed with relational options")
	}
	d.selectorOpen = true
	d.selectorDraft = query
	d.selectorResponseOpen = true
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 1500, 800)
	_, preview := tt.Find("原生搜索响应预览")
	if !preview || !strings.Contains(strings.Join(tt.Texts(), "\n"), "响应预览已截断") {
		t.Fatal("native response not explicitly bounded")
	}
	if err := tt.Click("复制完整搜索响应"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != response || calls.Load() != 1 {
		t.Fatal("response copy was truncated or replayed the search")
	}
	entry := d.tools.history[0]
	if !strings.Contains(mysqlDataHistoryRules(entry), "Meilisearch 搜索") {
		t.Fatal("native history displayed relational contains semantics")
	}
	page, request := d.page, d.request
	s.restoreDataHistory(d, entry)
	if d.selectorResponseOpen || !d.selectorOpen || d.selectorDraft != query || d.page != page || d.request != request || calls.Load() != 1 || d.loading {
		t.Fatal("native history did not restore input without replay")
	}
	s.applyDataSelector(ui.Services{}, d)
	wait()
	<-requests
	if calls.Load() != 2 || d.after != nil || d.offset != 0 {
		t.Fatal("explicit history apply reused a stale boundary")
	}
	s.Close(d)
	if d.selectorResponseOpen || d.selectorDraft != "" || d.page != nil || s.selectorFor(d).Validate != nil {
		t.Fatal("closed search document retained response or callbacks")
	}
}
