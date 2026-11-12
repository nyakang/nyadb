package kvworkspace

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sessionBrowserReceipt(rows []contract.KVHTTPSession) *Receipt {
	r := &Receipt{Scope: testScope()}
	r.Scope.HTTP.Protocol = "consul-http"
	r.Result.HTTP = &contract.KVHTTPResult{Protocol: "HTTP/1.1", Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/session/list"}, Status: 200, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Sessions: &contract.KVHTTPSessionList{Rows: rows}}
	return r
}

func TestHTTPSessionBrowserPagingSelectionAndReceiptReplacementStayOriginal(t *testing.T) {
	rows := make([]contract.KVHTTPSession, 121)
	for i := range rows {
		rows[i] = contract.KVHTTPSession{ID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", 121-i), Name: fmt.Sprint(i), Checks: []string{"original"}, ServiceChecks: []contract.KVHTTPSessionServiceCheck{{ID: "check", PresentFields: []string{"ID"}}}}
	}
	r := sessionBrowserReceipt(rows)
	original := r.Clone()
	var b httpSessionBrowserState
	b.bind(r)
	if b.pageCount() != 3 || len(b.pageIndices()) != 50 || b.indices[0] != 0 || b.indices[120] != 120 {
		t.Fatal("local index changed original order or window size")
	}
	if !b.movePage(r, 2) || len(b.pageIndices()) != 21 || !b.selectRow(r, 2, 20) {
		t.Fatal("last captured window could not select original final UUID")
	}
	selected, ok := b.selected(r)
	if !ok || selected.ID != rows[120].ID {
		t.Fatal("selection used a page-relative index as original identity")
	}
	selected.Checks[0] = "changed"
	selected.ServiceChecks[0].PresentFields[0] = "changed"
	if !reflect.DeepEqual(*r, original) {
		t.Fatal("selected detail aliases or mutates original complete receipt")
	}
	if b.selectRow(r, 1, 0) || b.movePage(r, 3) || b.movePage(r, -1) {
		t.Fatal("stale page event or unavailable window accepted")
	}
	next := r.Clone()
	if b.selectRow(&next, 2, 0) {
		t.Fatal("another receipt acquired original selection")
	}
	b.bind(&next)
	if _, ok := b.selected(&next); ok || b.page != 0 {
		t.Fatal("replacement receipt inherited an old selected UUID")
	}
	b.close()
	if b.receipt != nil || b.indices != nil || b.selectedID != "" {
		t.Fatal("close retained original Session receipt or index")
	}
}

func TestHTTPSessionBrowserTTLFiltersDoNotInventTTLFromMissingOrNull(t *testing.T) {
	rows := []contract.KVHTTPSession{
		{ID: "missing", TTL: "10m"},
		{ID: "null", PresentFields: []string{"TTL"}, NullFields: []string{"TTL"}},
		{ID: "empty", PresentFields: []string{"TTL"}},
		{ID: "zero", PresentFields: []string{"TTL"}, TTL: "0s"},
		{ID: "timed", Name: "中文 Name", PresentFields: []string{"TTL"}, TTL: "10m0s"},
		{ID: "invalid", PresentFields: []string{"TTL"}, TTL: "native future value"},
	}
	r := sessionBrowserReceipt(rows)
	b := httpSessionBrowserState{ttl: httpSessionTTLUnavailable}
	b.bind(r)
	if !reflect.DeepEqual(b.indices, []int{0, 1, 5}) {
		t.Fatal("unavailable original TTLs were converted into absent timers", b.indices)
	}
	b.ttl = httpSessionTTLNone
	b.bind(r)
	if !reflect.DeepEqual(b.indices, []int{2, 3}) {
		t.Fatal("explicit no-TTL values were lost", b.indices)
	}
	b.ttl = httpSessionTTLConfigured
	b.bind(r)
	if !reflect.DeepEqual(b.indices, []int{4}) || !b.selectRow(r, 0, 0) {
		t.Fatal("configured TTL observation missing")
	}
	b.query = "中文 nAmE"
	if _, ok := b.selected(r); ok || b.selectRow(r, 0, 0) {
		t.Fatal("filter edit kept stale row selection before recomputing")
	}
	b.bind(r)
	if len(b.indices) != 1 || b.selectedID != "" {
		t.Fatal("local Unicode search lost original record or selected implicitly")
	}
	b.query = "no match"
	b.bind(r)
	if b.pageCount() != 1 || len(b.pageIndices()) != 0 || b.selectRow(r, 0, 0) {
		t.Fatal("empty local filter invented a selected Session")
	}
	if len(r.Result.HTTP.Sessions.Rows) != 6 {
		t.Fatal("local filtering truncated complete response")
	}
}

func TestHTTPSessionBrowserRejectsIncompleteAndForeignReceiptProjections(t *testing.T) {
	r := sessionBrowserReceipt([]contract.KVHTTPSession{{ID: "original"}})
	for _, mutate := range []func(*Receipt){
		func(r *Receipt) { r.Scope.HTTP.Protocol = "etcd" },
		func(r *Receipt) { r.Result.HTTP.Request.Method = "PUT" },
		func(r *Receipt) { r.Result.HTTP.Request.Path = "/v1/catalog/nodes" },
		func(r *Receipt) { r.Result.HTTP.Request.Path = "/v1/session/info/" },
		func(r *Receipt) { r.Result.WriteOutcomeUnknown = true },
		func(r *Receipt) { r.Result.HTTP.Acknowledged = false },
		func(r *Receipt) { r.Result.HTTP.EOF = false },
		func(r *Receipt) { r.Result.HTTP.Status = 403 },
		func(r *Receipt) { r.Result.HTTP.Sessions = nil },
	} {
		copy := r.Clone()
		mutate(&copy)
		var b httpSessionBrowserState
		b.bind(&copy)
		if len(b.indices) != 0 || b.selectRow(&copy, 0, 0) {
			t.Fatal("foreign or incomplete receipt became a complete Session observation")
		}
	}
}
