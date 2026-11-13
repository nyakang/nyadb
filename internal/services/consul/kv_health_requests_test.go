package consul

import (
	"math"
	"net/url"
	"strconv"
	"testing"
)

func TestHealthRequestsPreserveExactTargetsPassingAndReadOptions(t *testing.T) {
	for _, state := range []string{"any", "passing", "warning", "critical"} {
		r, err := HealthStateRequest(state)
		if err != nil || r.Method != "GET" || r.Path != "/v1/health/state/"+state {
			t.Fatal("original health state changed", err)
		}
	}
	name := "服务/中文 %?#"
	for _, builder := range []func(string) (Request, error){HealthNodeRequest, HealthChecksRequest} {
		r, err := builder(name)
		if err != nil || r.Path[len(r.Path)-len(url.PathEscape(name)):] != url.PathEscape(name) {
			t.Fatal("qualified original health path changed", err)
		}
	}
	for _, passing := range []*bool{nil, new(false), new(true)} {
		r, err := HealthServiceRequest(name, passing)
		if err != nil {
			t.Fatal(err)
		}
		if passing == nil && r.Query.Has("passing") || passing != nil && r.Query.Get("passing") != strconv.FormatBool(*passing) {
			t.Fatal("passing absent/false/true conflated")
		}
		filter, near, wait := "Node == `原节点`", "_agent", "1s"
		index := uint64(math.MaxUint64)
		captured, err := WithHealthReadOptions(r, HealthReadOptions{Filter: &filter, Near: &near, Index: &index, Wait: &wait})
		if err != nil {
			t.Fatal(err)
		}
		filter, near, wait, index = "changed", "changed", "2s", 0
		if captured.Query.Get("filter") != "Node == `原节点`" || captured.Query.Get("near") != "_agent" || captured.Query.Get("wait") != "1s" || captured.Query.Get("index") != "18446744073709551615" {
			t.Fatal("read options borrowed mutable inputs")
		}
		if r.Query.Has("filter") || r.Query.Has("index") {
			t.Fatal("option capture modified original request map")
		}
		document, err := DocumentRequest(captured)
		if err != nil {
			t.Fatal(err)
		}
		native, canonical, writes, err := decodeDocumentRequest(document)
		if err != nil || writes || canonical != document || native.Query.Get("index") != "18446744073709551615" {
			t.Fatal("native read roundtrip changed scope/options", err)
		}
	}
}

func TestHealthRequestsRejectInvalidScopesAndUnsupportedOptionReuse(t *testing.T) {
	for _, state := range []string{"", "PASSING", "passing ", "unknown"} {
		if _, err := HealthStateRequest(state); err == nil {
			t.Fatal("invalid health state accepted")
		}
	}
	for _, builder := range []func(string) (Request, error){HealthNodeRequest, HealthChecksRequest} {
		for _, name := range []string{"", "a\x00b"} {
			if _, err := builder(name); err == nil {
				t.Fatal("unqualified target accepted")
			}
		}
	}
	r, _ := HealthStateRequest("any")
	wait := "1s"
	if _, err := WithHealthReadOptions(r, HealthReadOptions{Wait: &wait}); err == nil {
		t.Fatal("wait without original index accepted")
	}
	if _, err := WithHealthReadOptions(Request{Method: "PUT", Path: "/v1/session/create"}, HealthReadOptions{}); err == nil {
		t.Fatal("read options applied to a write")
	}
	for _, path := range []string{"/v1/kv/key", "/v1/session/list"} {
		if _, _, _, err := validateRequest(Request{Method: "GET", Path: path, Query: url.Values{"filter": {"Status == `passing`"}}}); err == nil {
			t.Fatal("health-only semantics expanded unrelated endpoint", path)
		}
	}
	r, _ = HealthServiceRequest("original", nil)
	r.Query = url.Values{"passing": {"FALSE"}}
	if _, _, _, err := validateRequest(r); err == nil {
		t.Fatal("noncanonical native passing boolean accepted")
	}
}
