package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func agentBrowserReceipt(count int, kind string) *Receipt {
	p := &contract.KVHTTPAgentList{Kind: kind, Rows: []contract.KVHTTPAgentEntry{}, ProjectionComplete: true, Complete: true}
	var body bytes.Buffer
	body.WriteByte('{')
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("original-agent-%03d", i)
		e := contract.KVHTTPAgentEntry{Key: id, ID: id, Service: "原服务", Name: fmt.Sprintf("原检查 %03d", i), Status: "passing", PresentFields: []string{"ID", "Service", "CheckID", "ServiceName", "Name", "Status", "Tags", "Meta"}, Tags: []string{}, Meta: map[string]string{"本机": "原值"}}
		field, service := "ID", "Service"
		if kind == "checks" {
			field, service = "CheckID", "ServiceName"
		}
		e.Raw = []byte(fmt.Sprintf(`{"%s":%q,"%s":"原服务","Name":%q,"Status":"passing","Tags":[],"future":18446744073709551615}`, field, id, service, e.Name))
		if i > 0 {
			body.WriteByte(',')
		}
		key, _ := json.Marshal(id)
		body.Write(key)
		body.WriteByte(':')
		body.Write(e.Raw)
		p.Rows = append(p.Rows, e)
	}
	body.WriteByte('}')
	return &Receipt{Result: contract.KVResult{Completed: true, HTTP: &contract.KVHTTPResult{Agent: p, Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/agent/" + kind}, Status: 200, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Body: body.Bytes()}}, Scope: contract.KVScope{HTTP: contract.KVHTTPIdentity{Protocol: "consul-http"}}}
}

func TestAgentBrowserOriginalMapPagingFilteringSelectionAndReceiptReplacement(t *testing.T) {
	r := agentBrowserReceipt(53, "checks")
	var b httpAgentBrowserState
	b.bind(r)
	if b.pageCount() != 2 || !b.movePage(r, 1) || len(b.pageIndices()) != 3 || !b.selectRow(r, 1, 2) {
		t.Fatal("original local map paging unavailable")
	}
	if index, ok := b.selectedIndex(r); !ok || index != 52 {
		t.Fatal("Agent selection lost original map position")
	}
	b.query = "原值"
	if _, ok := b.selectedIndex(r); ok {
		t.Fatal("changed filter retained stale selection")
	}
	b.bind(r)
	if len(b.indices) != 53 || b.page != 0 || b.selectedOriginal != -1 {
		t.Fatal("local metadata search discarded map rows or retained page")
	}
	b.status = httpHealthCritical
	b.bind(r)
	if len(b.indices) != 0 {
		t.Fatal("native passing checks matched critical filter")
	}
	replacement := agentBrowserReceipt(1, "services")
	b.query, b.detail, b.preview = "", true, "old detail"
	b.bind(replacement)
	if len(b.indices) != 1 || b.detail || b.preview != "" || b.selectedOriginal != -1 || b.current(r) {
		t.Fatal("new original receipt inherited old detail/selection")
	}
	b.close()
	if b.receipt != nil || b.indices != nil {
		t.Fatal("Agent browser close retained original cache")
	}
}

func TestAgentToolDraftsRetainPartialFilterAndOriginalUnknownWrite(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
		d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	}
	kind := contract.KVToolHTTPAgentChecks
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPAgent: contract.KVHTTPAgentToolInput{IncludeFilter: true, Filter: "partial ` 原值"}}
	s.SetRequestToolInput(d, kind, input)
	original := d.Request()
	raw, err := encodeRequestToolInput(kind, input)
	var captured struct{ Input contract.KVRequestToolInput }
	if err != nil || json.Unmarshal(raw, &captured) != nil || captured.Input != input {
		t.Fatal("partial Agent filter lost in draft export", err)
	}
	s.OpenRequestTool(d, contract.KVToolHTTPAgentSelf)
	s.OpenRequestTool(d, kind)
	got, _ := d.RequestToolInput(kind)
	if got != input || d.Request() != original || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("Agent form switch changed original request or executed")
	}
	bad := input
	bad.HTTPAgent.Filter = strings.Repeat("x", inputLimit+1)
	s.SetRequestToolInput(d, kind, bad)
	got, _ = d.RequestToolInput(kind)
	if got != input || d.Error() == "" {
		t.Fatal("Agent field bypassed complete draft budget")
	}
	bad.HTTPAgent.Filter = string([]byte{255})
	if _, err := encodeRequestToolInput(kind, bad); err == nil {
		t.Fatal("Agent draft export normalized invalid Unicode")
	}
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, HTTP: &contract.KVHTTPResult{Body: []byte("original unknown write")}}}
	unknown := d.unknown
	s.GenerateRequestTool(d)
	if d.Request() == original || d.unknown != unknown || !bytes.Equal(d.unknown.Result.HTTP.Body, []byte("original unknown write")) || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("Agent text generation changed or replayed original unknown write")
	}
}
