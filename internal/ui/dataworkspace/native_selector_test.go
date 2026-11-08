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

func TestDataSelectorInvalidCacheImmutableDispatchAndHistoryWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan contract.DataOptions, 4)
	active := Session{ProfileID: 7, Generation: 1, Connected: true}
	r := Runtime{Capabilities: contract.Capabilities{Documents: true}, SortColumns: []string{"_id"}, Selector: contract.DataSelector{Dialect: "mango", Column: "document", Mode: "mango", Validate: func(raw string) error {
		if !json.Valid([]byte(raw)) || !strings.HasPrefix(raw, "{") {
			return errors.New("invalid selector")
		}
		return nil
	}}, ReadDescription: func(contract.DataOptions) string { return "POST /owned/_find" }, AdvancedLoader: func(_ context.Context, _ int64, _, _ string, o contract.DataOptions) (*contract.DataPage, error) {
		calls.Add(1)
		requests <- o
		return &contract.DataPage{Object: o.Object, Columns: []string{"_id", "_rev", "document"}, ColumnKinds: []string{"text", "text", "json"}, Rows: [][]contract.DataCell{{{Text: "original"}, {Text: "revision"}, {Text: o.FilterValue}}}, Offset: o.Offset, Limit: o.Limit, QuerySQL: "POST /owned/_find"}, nil
	}}
	s := New(Dependencies{Session: func() Session { return active }, Runtime: func() Runtime { return r }})
	d := s.OpenObject(1, (contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}).Object("documents", "_all_docs"), r.Capabilities)
	defer s.Close(d)
	d.page = &contract.DataPage{Columns: []string{"document"}, Rows: [][]contract.DataCell{{{Text: "cached"}}}}
	original, request := d.page, d.request
	d.selectorDraft = "["
	s.applyDataSelector(ui.Services{}, d)
	if d.selectorError == "" || d.page != original || d.request != request || d.loading || calls.Load() != 0 {
		t.Fatal("invalid selector changed cache or dispatched I/O")
	}
	const raw = `{"n":{"$eq":9007199254740993}}`
	d.selectorDraft = raw
	d.filterRules = []contract.DataFilterRule{{Column: "_id", Mode: "equal", Value: "old"}}
	d.sortRules = []contract.DataSortRule{{Column: "_id", Descending: true}}
	s.applyDataSelector(ui.Services{}, d)
	d.selectorDraft = "changed after dispatch"
	end := time.Now().Add(time.Second)
	for d.loading && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.loading || d.err != "" || d.page == nil {
		t.Fatal("native selector read did not settle", d.err)
	}
	opts := <-requests
	if opts.FilterColumn != "document" || opts.FilterMode != "mango" || opts.FilterValue != raw || len(opts.Filters) != 0 || opts.Condition != nil || len(opts.Sorts) != 1 || !opts.Sorts[0].Descending {
		t.Fatal("native selector request lost immutable values or mixed filter representations")
	}
	entry := d.tools.history[0]
	if entry.SQL != "POST /owned/_find" || entry.Summary || !strings.HasPrefix(s.dataHistoryLabel(d, entry, 0), "READ") {
		t.Fatal("native request history inferred SQL")
	}
	page, request := d.page, d.request
	s.restoreDataHistory(d, entry)
	entry.Sorts[0].Descending = false
	if !d.selectorOpen || d.selectorDraft != raw || d.page != page || d.request != request || d.loading || calls.Load() != 1 || !d.selectorHistorySorts[0].Descending {
		t.Fatal("history replayed a query or shared mutable sort drafts")
	}
	s.applyDataSelector(ui.Services{}, d)
	end = time.Now().Add(time.Second)
	for d.loading && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	request = d.request
	s.syncMySQLDataSort(ui.Services{}, d)
	if d.loading || d.request != request || calls.Load() != 2 || d.sort.Column != "_id" || !d.sort.Descending || len(d.sortRules) != 1 || !d.sortRules[0].Descending {
		t.Fatal("restored selector sort triggered an implicit second query or lost order")
	}
	s.Close(d)
	if d.selectorDraft != "" || d.selectorHistorySorts != nil || s.selectorFor(d).Dialect != "" {
		t.Fatal("closed document retained native selector draft/callback")
	}
}

func TestDataSelectorUnavailableFeatureCannotDispatch(t *testing.T) {
	s := New(Dependencies{})
	d := s.OpenObject(1, (contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}).Object("documents", "_all_docs"), contract.Capabilities{Documents: true})
	defer s.Close(d)
	d.selectorDraft = `{"_id":"original"}`
	s.applyDataSelector(ui.Services{}, d)
	if d.loading || d.filterMode != "" {
		t.Fatal("unavailable selector fell back to another runtime")
	}
}
