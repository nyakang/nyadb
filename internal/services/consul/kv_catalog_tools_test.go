package consul

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestCatalogToolsPreserveOriginalNamesOptionsAndOfflineReadOnlyAuthority(t *testing.T) {
	s := &Session{closed: true, secret: "never-export-this-fixture", cfg: Config{Endpoint: "https://foreign.invalid", Prefix: "foreign-prefix/"}}
	builder := s.KVRequestBuilder()
	name := "  original 雪%?#++  "
	cases := []struct {
		kind  contract.KVRequestToolKind
		input contract.KVHTTPCatalogToolInput
		path  string
	}{
		{contract.KVToolHTTPCatalogDatacenters, contract.KVHTTPCatalogToolInput{}, "/v1/catalog/datacenters"},
		{contract.KVToolHTTPCatalogNodes, contract.KVHTTPCatalogToolInput{}, "/v1/catalog/nodes"},
		{contract.KVToolHTTPCatalogServices, contract.KVHTTPCatalogToolInput{}, "/v1/catalog/services"},
		{contract.KVToolHTTPCatalogService, contract.KVHTTPCatalogToolInput{Service: name}, "/v1/catalog/service/" + url.PathEscape(name)},
		{contract.KVToolHTTPCatalogNode, contract.KVHTTPCatalogToolInput{Node: name}, "/v1/catalog/node/" + url.PathEscape(name)},
	}
	for _, c := range cases {
		h := c.input
		if c.kind != contract.KVToolHTTPCatalogDatacenters {
			h.IncludeFilter, h.Filter, h.Index, h.Wait = true, "ServiceName == `原服务`", "18446744073709551615", "1ms"
			if c.kind == contract.KVToolHTTPCatalogNodes || c.kind == contract.KVToolHTTPCatalogService {
				h.IncludeNear, h.Near = true, ""
			}
		}
		input := contract.KVRequestToolInput{HTTPCatalog: h}
		before := input
		request, err := builder.BuildKVRequest(c.kind, input)
		if err != nil {
			t.Fatal(err)
		}
		native, canonical, writes, err := decodeDocumentRequest(request)
		var want url.Values
		if c.kind != contract.KVToolHTTPCatalogDatacenters {
			want = url.Values{}
			want.Set("filter", h.Filter)
			want.Set("index", h.Index)
			want.Set("wait", h.Wait)
			if h.IncludeNear {
				want.Set("near", "")
			}
		}
		if err != nil || writes || request != canonical || native.Method != "GET" || native.Path != c.path || native.Body != nil || !reflect.DeepEqual(native.Query, want) || input != before || strings.Contains(request.JSON, s.secret) || strings.Contains(request.JSON, "foreign.invalid") || strings.Contains(request.JSON, "foreign-prefix/") {
			t.Fatal("Catalog builder changed original scope/name/presence or gained I/O authority", err)
		}
	}
	for _, filter := range []struct {
		include bool
		text    string
	}{{false, ""}, {true, ""}, {true, "original"}} {
		request, err := builder.BuildKVRequest(contract.KVToolHTTPCatalogServices, contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{IncludeFilter: filter.include, Filter: filter.text}})
		if err != nil {
			t.Fatal(err)
		}
		native, _, _, err := decodeDocumentRequest(request)
		if err != nil || native.Query.Has("filter") != filter.include || native.Query.Get("filter") != filter.text {
			t.Fatal("Catalog empty/omitted filter conflated")
		}
	}
}

func TestCatalogToolsRejectHiddenForeignIgnoredAndRewrittenInputs(t *testing.T) {
	base := contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{Service: "original"}}
	for _, mutate := range []func(*contract.KVRequestToolInput){
		func(p *contract.KVRequestToolInput) { p.Key = "hidden" }, func(p *contract.KVRequestToolInput) { p.ValueBase64 = "AA==" }, func(p *contract.KVRequestToolInput) { p.Flags = "0" }, func(p *contract.KVRequestToolInput) { p.CompareIndex = "0" }, func(p *contract.KVRequestToolInput) { p.KeysOnly = true }, func(p *contract.KVRequestToolInput) { p.Recursive = true }, func(p *contract.KVRequestToolInput) { p.TransactionJSON = "[]" }, func(p *contract.KVRequestToolInput) { p.HTTPSession.ID = sessionToolID }, func(p *contract.KVRequestToolInput) { p.HTTPAgent.IncludeFilter = true }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.State = "passing" }, func(p *contract.KVRequestToolInput) { p.HTTPAgentMaintenance.Enable = "true" },
		func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Node = "foreign" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Filter = "hidden" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Near = "hidden" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Index = "01" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Index = "18446744073709551616" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Wait = "1s" }, func(p *contract.KVRequestToolInput) { p.HTTPCatalog.Index = "0"; p.HTTPCatalog.Wait = "11m" },
	} {
		p := base
		mutate(&p)
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPCatalogService, p); err == nil {
			t.Fatal("hidden or invalid Catalog semantics accepted")
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolPut, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentNodeMaintenance} {
		if _, err := (requestBuilder{}).BuildKVRequest(kind, base); err == nil {
			t.Fatal("Catalog inputs expanded unrelated tool", kind)
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogNode} {
		input := contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{IncludeNear: true, Near: "_agent"}}
		if kind == contract.KVToolHTTPCatalogNode {
			input.HTTPCatalog.Node = "node"
		}
		if _, err := (requestBuilder{}).BuildKVRequest(kind, input); !errors.Is(err, ErrUnsupported) {
			t.Fatal("Catalog tool advertised ignored near ordering", kind, err)
		}
	}
	for _, input := range []contract.KVHTTPCatalogToolInput{{IncludeFilter: true}, {IncludeNear: true}, {Index: "0"}, {Node: "node"}, {Service: "svc"}} {
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPCatalogDatacenters, contract.KVRequestToolInput{HTTPCatalog: input}); err == nil {
			t.Fatal("global datacenters accepted hidden scope/filter")
		}
	}
	for _, name := range []string{".", "..", "/leading", "trailing/", "a//b", "a/../b"} {
		p := base
		p.HTTPCatalog.Service = name
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPCatalogService, p); !errors.Is(err, ErrUnsupported) {
			t.Fatal("Catalog tool changed an original noncanonical target", err)
		}
	}
}

func TestCatalogToolsPresentationRetainsThirtyExistingToolsWithoutPrefixDefaults(t *testing.T) {
	s := &Session{cfg: Config{Prefix: "original/"}}
	tools := s.KVRequestPresentation().Tools
	if len(tools) != 35 || tools[28].Kind != contract.KVToolHTTPAgentNodeMaintenance || tools[29].Kind != contract.KVToolHTTPAgentServiceMaintenance {
		t.Fatal("Catalog publication changed retained tools")
	}
	for i, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPCatalogDatacenters, contract.KVToolHTTPCatalogNodes, contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogService, contract.KVToolHTTPCatalogNode} {
		if tools[30+i].Kind != kind || tools[30+i].Default != (contract.KVRequestToolInput{}) {
			t.Fatal("Catalog defaults invented a target or inherited KV prefix")
		}
	}
	tools[30].Default.HTTPCatalog.Filter = "changed"
	if s.KVRequestPresentation().Tools[30].Default.HTTPCatalog != (contract.KVHTTPCatalogToolInput{}) {
		t.Fatal("Catalog presentation shares mutable defaults")
	}
}
