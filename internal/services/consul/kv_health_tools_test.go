package consul

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestHealthToolsCaptureExactOriginalTargetsOptionsAndOfflineReadOnlyBuilder(t *testing.T) {
	s := &Session{closed: true, secret: "must-not-appear", cfg: Config{Endpoint: "https://foreign.invalid", Prefix: "configured/"}}
	b := s.KVRequestBuilder()
	name := "服务/中文 %?#"
	cases := []struct {
		kind contract.KVRequestToolKind
		h    contract.KVHTTPHealthToolInput
		path string
	}{
		{contract.KVToolHTTPHealthState, contract.KVHTTPHealthToolInput{State: "critical"}, "/v1/health/state/critical"},
		{contract.KVToolHTTPHealthNode, contract.KVHTTPHealthToolInput{Node: name}, "/v1/health/node/" + url.PathEscape(name)},
		{contract.KVToolHTTPHealthChecks, contract.KVHTTPHealthToolInput{Service: name}, "/v1/health/checks/" + url.PathEscape(name)},
		{contract.KVToolHTTPHealthService, contract.KVHTTPHealthToolInput{Service: name, Passing: "false"}, "/v1/health/service/" + url.PathEscape(name)},
	}
	for _, c := range cases {
		h := c.h
		h.IncludeFilter, h.Filter, h.IncludeNear, h.Near, h.Index, h.Wait = true, "Node == `原节点`", true, "", "18446744073709551615", "1ms"
		input := contract.KVRequestToolInput{HTTPHealth: h}
		original := input
		r, err := b.BuildKVRequest(c.kind, input)
		if err != nil {
			t.Fatal(err)
		}
		native, canonical, writes, err := decodeDocumentRequest(r)
		want := url.Values{"filter": {"Node == `原节点`"}, "near": {""}, "index": {"18446744073709551615"}, "wait": {"1ms"}}
		if c.h.Passing != "" {
			want.Set("passing", c.h.Passing)
		}
		if err != nil || writes || r != canonical || native.Method != "GET" || native.Path != c.path || native.Body != nil || !reflect.DeepEqual(native.Query, want) || input != original || strings.Contains(r.JSON, s.secret) || strings.Contains(r.JSON, "foreign.invalid") || strings.Contains(r.JSON, "configured/") {
			t.Fatal("health builder borrowed mutable scope, changed original inputs or wrote", err)
		}
	}
	for _, passing := range []string{"", "false", "true"} {
		r, err := b.BuildKVRequest(contract.KVToolHTTPHealthService, contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Service: "original", Passing: passing}})
		if err != nil {
			t.Fatal(err)
		}
		native, _, _, err := decodeDocumentRequest(r)
		if err != nil || native.Query.Has("passing") != (passing != "") || native.Query.Get("passing") != passing {
			t.Fatal("passing omission and explicit boolean conflated")
		}
	}
}

func TestHealthToolsRejectHiddenWritesForeignParametersAndRewrittenPaths(t *testing.T) {
	base := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Service: "original"}}
	for _, mutate := range []func(*contract.KVRequestToolInput){
		func(p *contract.KVRequestToolInput) { p.Key = "hidden" }, func(p *contract.KVRequestToolInput) { p.ValueBase64 = "AA==" }, func(p *contract.KVRequestToolInput) { p.Flags = "0" }, func(p *contract.KVRequestToolInput) { p.CompareIndex = "0" }, func(p *contract.KVRequestToolInput) { p.Recursive = true }, func(p *contract.KVRequestToolInput) { p.KeysOnly = true }, func(p *contract.KVRequestToolInput) { p.TransactionJSON = "[]" }, func(p *contract.KVRequestToolInput) { p.HTTPSession.ID = sessionToolID },
		func(p *contract.KVRequestToolInput) { p.HTTPHealth.Node = "foreign" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.State = "passing" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Filter = "hidden" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Near = "hidden" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Index = "01" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Index = "18446744073709551616" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Wait = "1s" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Index = "0"; p.HTTPHealth.Wait = "11m" }, func(p *contract.KVRequestToolInput) { p.HTTPHealth.Passing = "FALSE" },
	} {
		p := base
		mutate(&p)
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPHealthService, p); err == nil {
			t.Fatal("hidden, invalid or foreign parameter accepted", p)
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolPut, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionList} {
		if _, err := (requestBuilder{}).BuildKVRequest(kind, base); err == nil {
			t.Fatal("health options expanded unrelated tool", kind)
		}
	}
	for _, name := range []string{".", "..", "/leading", "trailing/", "one//two", "one/../two", "one/./two"} {
		p := base
		p.HTTPHealth.Service = name
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPHealthService, p); !errors.Is(err, ErrUnsupported) {
			t.Fatal("server-rewritten original target accepted", name, err)
		}
	}
	p := base
	p.HTTPHealth.IncludeFilter = true
	p.HTTPHealth.Filter = strings.Repeat("x", 4097)
	if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPHealthChecks, p); !errors.Is(err, ErrBounds) {
		t.Fatal("bounded native option accepted", err)
	}
}

func TestHealthToolsPresentationRetainsThirteenOriginalToolsAndOriginalNodeSuggestion(t *testing.T) {
	s := &Session{cfg: Config{Prefix: "original/"}, scope: contract.KVScope{HTTP: contract.KVHTTPIdentity{AgentName: "original-node"}}}
	tools := s.KVRequestPresentation().Tools
	if len(tools) != 30 {
		t.Fatal("health tools not published")
	}
	want := []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolList, contract.KVToolRaw, contract.KVToolPut, contract.KVToolDelete, contract.KVToolTransaction, contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease, contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions, contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService}
	for i, kind := range want {
		if tools[i].Kind != kind {
			t.Fatal("original tool ordering changed", i)
		}
	}
	if tools[13].Default.HTTPHealth.State != "any" || tools[14].Default.HTTPHealth.Node != "original-node" || tools[15].Default.HTTPHealth.Service != "" || tools[16].Default.HTTPHealth.Service != "" {
		t.Fatal("health defaults invented a service or lost original node")
	}
	tools[14].Default.HTTPHealth.Node = "changed"
	if s.KVRequestPresentation().Tools[14].Default.HTTPHealth.Node != "original-node" {
		t.Fatal("shared presentation mutated")
	}
}
