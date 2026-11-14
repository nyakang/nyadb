package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func catalogBrowserReceipt(count int, kind string) *Receipt {
	p := &contract.KVHTTPCatalogList{Kind: kind, ProjectionComplete: true, Complete: true}
	h := &contract.KVHTTPResult{Catalog: p, Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/catalog/" + kind}, Status: 200, Captured: true, EOF: true, Acknowledged: true, Succeeded: true}
	var body bytes.Buffer
	if kind == "services" || kind == "node" {
		body.WriteByte('{')
	} else {
		body.WriteByte('[')
	}
	if kind == "node" {
		n := contract.KVHTTPCatalogNode{Node: "原节点", Datacenter: "dc1", Meta: map[string]string{"original": "原值"}, PresentFields: []string{"Node", "Datacenter", "Meta"}, Raw: []byte(`{"Node":"原节点","Datacenter":"dc1","Meta":{"original":"原值"}}`)}
		p.NodeServices = &contract.KVHTTPCatalogNodeServices{Node: n, Services: []contract.KVHTTPCatalogNodeService{}, PresentFields: []string{"Node", "Services"}}
		body.WriteString(`"Node":`)
		body.Write(n.Raw)
		body.WriteString(`,"Services":{`)
		h.Request.Path = "/v1/catalog/node/" + url.PathEscape(n.Node)
	}
	if kind == "service" {
		h.Request.Path = "/v1/catalog/service/" + url.PathEscape("原服务")
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("original-catalog-%03d", i)
		if i > 0 {
			body.WriteByte(',')
		}
		switch kind {
		case "datacenters":
			p.Datacenters = append(p.Datacenters, id)
			// Preserve an original escape spelling in the selected JSON value.
			raw := fmt.Sprintf(`"original-catalog-\u0030%02d"`, i)
			body.WriteString(raw)
		case "nodes":
			raw := []byte(fmt.Sprintf(`{"Node":%q,"ID":%q,"Datacenter":"dc1","Meta":{"original":"原值"},"ModifyIndex":18446744073709551615,"future":9007199254740993}`, id, id))
			p.Nodes = append(p.Nodes, contract.KVHTTPCatalogNode{Node: id, ID: id, Datacenter: "dc1", Meta: map[string]string{"original": "原值"}, ModifyIndex: ^uint64(0), PresentFields: []string{"Node", "ID", "Datacenter", "Meta", "ModifyIndex"}, Raw: raw})
			body.Write(raw)
		case "services":
			raw := []byte(`["z 原值", "a", "z 原值"]`)
			p.Services = append(p.Services, contract.KVHTTPCatalogServiceTags{Name: id, Tags: []string{"z 原值", "a", "z 原值"}, Raw: raw})
			key, _ := json.Marshal(id)
			body.Write(key)
			body.WriteByte(':')
			body.Write(raw)
		case "service":
			raw := []byte(fmt.Sprintf(`{"Node":%q,"ServiceID":%q,"ServiceName":"原服务","ServiceMeta":{"original":"原值"},"ModifyIndex":18446744073709551615,"future":9007199254740993}`, id, id))
			p.Instances = append(p.Instances, contract.KVHTTPCatalogInstance{Node: id, ServiceID: id, ServiceName: "原服务", ServiceMeta: map[string]string{"original": "原值"}, ModifyIndex: ^uint64(0), PresentFields: []string{"Node", "ServiceID", "ServiceName", "ServiceMeta", "ModifyIndex"}, Raw: raw})
			body.Write(raw)
		case "node":
			raw := []byte(fmt.Sprintf(`{"ID":%q,"Service":"原服务","Meta":{"original":"原值"},"future":9007199254740993}`, id))
			service := contract.KVHTTPHealthService{ID: id, Service: "原服务", Meta: map[string]string{"original": "原值"}, PresentFields: []string{"ID", "Service", "Meta"}}
			p.NodeServices.Services = append(p.NodeServices.Services, contract.KVHTTPCatalogNodeService{Key: id, Service: service, Raw: raw})
			key, _ := json.Marshal(id)
			body.Write(key)
			body.WriteByte(':')
			body.Write(raw)
		}
	}
	if kind == "node" {
		body.WriteString("}}")
		p.NodeServices.Raw = bytes.Clone(body.Bytes())
	} else if kind == "services" {
		body.WriteByte('}')
	} else {
		body.WriteByte(']')
	}
	h.Body = body.Bytes()
	return &Receipt{Result: contract.KVResult{Completed: true, HTTP: h}, Scope: contract.KVScope{HTTP: contract.KVHTTPIdentity{Protocol: "consul-http"}}}
}

func TestCatalogBrowserOriginalPagingFilteringSelectionRawAndReplacement(t *testing.T) {
	for _, kind := range []string{"datacenters", "nodes", "services", "service", "node"} {
		t.Run(kind, func(t *testing.T) {
			r := catalogBrowserReceipt(53, kind)
			var b httpCatalogBrowserState
			b.bind(r)
			count := 53
			if kind == "node" {
				count++
			}
			if b.pageCount() != 2 || !b.movePage(r, 1) || len(b.pageIndices()) != count-50 || !b.selectRow(r, 1, 2) {
				t.Fatal("Catalog original paging unavailable")
			}
			index, ok := b.selectedIndex(r)
			if !ok || index != 52 {
				t.Fatal("Catalog selection lost original receipt ordinal")
			}
			raw, ok := catalogRowRaw(r, index)
			if !ok || !json.Valid(raw) {
				t.Fatal("selected original Catalog raw value unavailable")
			}
			if kind == "datacenters" && !bytes.Contains(raw, []byte(`\u0030`)) {
				t.Fatal("Catalog selected string was re-encoded")
			}
			b.query = "original-catalog-052"
			if _, ok := b.selectedIndex(r); ok {
				t.Fatal("changed search retained stale selection")
			}
			b.bind(r)
			if len(b.indices) != 1 || b.page != 0 || b.selectedOriginal != -1 {
				t.Fatal("Catalog local search lost exact identity or retained stale page")
			}
			if !b.selectRow(r, 0, 0) {
				t.Fatal("filtered original row cannot be selected")
			}
			b.detail, b.preview = true, "old"
			replacement := catalogBrowserReceipt(1, kind)
			b.query = ""
			b.bind(replacement)
			if b.detail || b.preview != "" || b.selectedOriginal != -1 || b.current(r) {
				t.Fatal("Catalog replacement inherited old selection/detail")
			}
			b.close()
			if b.receipt != nil || b.indices != nil {
				t.Fatal("Catalog browser close retained original cache")
			}
		})
	}
	r := catalogBrowserReceipt(1, "node")
	r.Result.HTTP.Catalog.NodeServices = nil
	r.Result.HTTP.Catalog.Null = true
	r.Result.HTTP.Body = []byte("null")
	if catalogReceiptProjection(r) == nil || catalogRowCount(r.Result.HTTP.Catalog) != 0 {
		t.Fatal("native Catalog root null lost its separate observation")
	}
}

func TestCatalogToolDraftsRetainOriginalPartialInputsUnknownWriteAndCompleteFiles(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	kinds := []contract.KVRequestToolKind{contract.KVToolHTTPCatalogDatacenters, contract.KVToolHTTPCatalogNodes, contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogService, contract.KVToolHTTPCatalogNode}
	for _, kind := range kinds {
		d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	}
	kind := contract.KVToolHTTPCatalogService
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{Service: "  原服务 %?#++  ", IncludeFilter: true, Filter: "partial ` 原值", IncludeNear: true, Near: "", Index: "18446744073709551615", Wait: "1ms"}}
	s.SetRequestToolInput(d, kind, input)
	original := d.Request()
	raw, err := encodeRequestToolInput(kind, input)
	var captured struct{ Input contract.KVRequestToolInput }
	if err != nil || json.Unmarshal(raw, &captured) != nil || captured.Input != input {
		t.Fatal("Catalog complete draft export changed authored fields", err)
	}
	s.OpenRequestTool(d, contract.KVToolHTTPCatalogDatacenters)
	s.OpenRequestTool(d, kind)
	got, _ := d.RequestToolInput(kind)
	if got != input || d.Request() != original || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("Catalog tool switch changed request or acquired I/O")
	}
	bad := input
	bad.HTTPCatalog.Near = strings.Repeat("x", inputLimit+1)
	s.SetRequestToolInput(d, kind, bad)
	got, _ = d.RequestToolInput(kind)
	if got != input || d.Error() == "" {
		t.Fatal("Catalog draft bypassed complete input budget")
	}
	bad.HTTPCatalog.Near = string([]byte{255})
	if _, err := encodeRequestToolInput(kind, bad); err == nil {
		t.Fatal("Catalog draft export rewrote invalid Unicode")
	}
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, HTTP: &contract.KVHTTPResult{Body: []byte("original unknown write")}}}
	unknown := d.unknown
	s.GenerateRequestTool(d)
	if d.Request() == original || d.unknown != unknown || !bytes.Equal(d.unknown.Result.HTTP.Body, []byte("original unknown write")) || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("Catalog generation changed or replayed original unknown write")
	}
}
