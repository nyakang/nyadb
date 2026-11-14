package consul

import (
	"errors"
	"math"
	"net/url"
	"reflect"
	"testing"
)

func TestCatalogReadOptionsPreserveOriginalNamesPresenceAndUnsignedIndices(t *testing.T) {
	name := "  original 雪%?#++  "
	service, err := CatalogServiceNodesRequest(name)
	if err != nil {
		t.Fatal(err)
	}
	node, err := CatalogNodeServicesRequest(name)
	if err != nil {
		t.Fatal(err)
	}
	filter, near, wait, empty := "ServiceName == \"original 雪\"", "_agent", "25ms", ""
	index := uint64(math.MaxUint64)
	for _, request := range []Request{CatalogNodesRequest(), CatalogServicesRequest(), service, node} {
		for _, authored := range []*string{nil, &empty, &filter} {
			request.Query = url.Values{"consistent": {""}}
			before := request.Clone()
			options := CatalogReadOptions{Filter: authored, Index: &index, Wait: &wait}
			if catalogReadKind(request.Path) == "nodes" || catalogReadKind(request.Path) == "service" {
				options.Near = &near
			}
			result, err := WithCatalogReadOptions(request, options)
			_, writes, _, validateErr := validateRequest(result)
			if err != nil || validateErr != nil || writes || result.Query.Has("filter") != (authored != nil) || result.Query.Get("index") != "18446744073709551615" || !result.Query.Has("consistent") || result.Query.Get("consistent") != "" || result.Path != request.Path || !reflect.DeepEqual(request, before) {
				t.Fatal("catalog options changed original target, scope, presence or precision", err, validateErr)
			}
			if authored != nil && result.Query.Get("filter") != *authored {
				t.Fatal("catalog filter text changed")
			}
			result.Query["consistent"][0] = "changed"
			if request.Query.Get("consistent") != "" {
				t.Fatal("catalog request retained caller-owned query bytes")
			}
		}
	}
	if service.Path != "/v1/catalog/service/"+url.PathEscape(name) || node.Path != "/v1/catalog/node/"+url.PathEscape(name) {
		t.Fatal("catalog named request trimmed or normalized its original name")
	}
}

func TestCatalogReadsRejectIgnoredOptionsForeignRoutesAndNoncanonicalNames(t *testing.T) {
	text, index := "_agent", uint64(0)
	node, _ := CatalogNodeServicesRequest("original")
	for _, request := range []Request{CatalogDatacentersRequest(), CatalogServicesRequest(), node} {
		if _, err := WithCatalogReadOptions(request, CatalogReadOptions{Near: &text}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("catalog exposed ignored distance ordering", request.Path, err)
		}
	}
	for _, options := range []CatalogReadOptions{{Filter: &text}, {Index: &index}, {Wait: &text}} {
		if _, err := WithCatalogReadOptions(CatalogDatacentersRequest(), options); !errors.Is(err, ErrUnsupported) {
			t.Fatal("global datacenter list accepted indexed or filter semantics", err)
		}
	}
	for _, build := range []func(string) (Request, error){CatalogServiceNodesRequest, CatalogNodeServicesRequest} {
		for _, name := range []string{"", "bad\nname", "a//b", "a/../b", "/leading", "trailing/", string([]byte{255})} {
			if _, err := build(name); err == nil {
				t.Fatal("catalog normalized an unsupported original name", name)
			}
		}
	}
	wait := "1s"
	if _, err := WithCatalogReadOptions(CatalogNodesRequest(), CatalogReadOptions{Wait: &wait}); !errors.Is(err, ErrInvalid) {
		t.Fatal("catalog wait accepted without original blocking index", err)
	}
	for _, request := range []Request{{Method: "PUT", Path: "/v1/catalog/nodes"}, {Method: "GET", Path: "/v1/agent/services"}, {Method: "GET", Path: "/v1/catalog/nodes", Body: []byte("hidden")}, {Method: "GET", Path: "/v1/catalog/nodes", Query: url.Values{"token": {"hidden"}}}, {Method: "GET", Path: "/v1/catalog/services", Query: url.Values{"near": {"_agent"}}}, {Method: "GET", Path: "/v1/catalog/nodes", Query: url.Values{"filter": {"one", "two"}}}} {
		if _, err := WithCatalogReadOptions(request, CatalogReadOptions{}); err == nil {
			t.Fatal("catalog options accepted foreign or incomplete request", request.Path)
		}
	}
}
