package consul

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func catalogProjectionResult(t *testing.T, path, body string) contract.KVResult {
	t.Helper()
	r := Request{Method: "GET", Path: path}
	request, err := DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	return documentResult(request, Response{Request: r, Kind: "read", Status: 200, Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Body: []byte(body)})
}

func assertCatalogOriginalReceipt(t *testing.T, result contract.KVResult, body string) {
	t.Helper()
	if result.HTTP == nil || !result.Completed || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || result.WriteOutcomeUnknown || !bytes.Equal(result.HTTP.Body, []byte(body)) || len(result.Native) != 0 {
		t.Fatal("Catalog projection changed original receipt or acknowledgement")
	}
	if result.HTTP.Catalog != nil {
		without := result.Clone()
		without.HTTP.Catalog = nil
		if resultCost(&result)-resultCost(&without) != catalogProjectionCost(*result.HTTP.Catalog) {
			t.Fatal("Catalog evidence missing from original lease accounting")
		}
	}
}

func TestCatalogProjectionPreservesOriginalShapesNamesOrderPresenceAndUnsignedIndices(t *testing.T) {
	node := `{"ID":"original-id","Node":"node 雪","Address":null,"Datacenter":"dc1","Meta":{"original":"值"},"TaggedAddresses":{},"CreateIndex":18446744073709551615,"ModifyIndex":9007199254740993,"future":{"unknown":[18446744073709551615]}}`
	service := `{"ID":"s:原","Service":"svc 雪","Port":65535,"Tags":[],"Meta":{"original":"值"},"Weights":{"Passing":3,"Warning":0},"future":9007199254740993}`
	instance := `{"ID":"original-id","Node":"node 雪","Address":"127.0.0.1","Datacenter":"dc1","ServiceID":"s:原","ServiceName":"svc 雪","ServicePort":65535,"ServiceTags":["z","a","z"],"NodeMeta":{},"ServiceMeta":{"original":"值"},"ServiceTaggedAddresses":{"lan":{"Address":"原","Port":1234,"future":true}},"ServiceWeights":{"Passing":18446744073709551615,"Warning":null},"ServiceEnableTagOverride":false,"Namespace":"","Partition":null,"ServiceProxy":{"Config":{"unknown":9007199254740993}},"CreateIndex":18446744073709551615,"ModifyIndex":9007199254740993}`
	cases := []struct{ path, body, kind string }{
		{"/v1/catalog/datacenters", `["dc2","dc1"]`, "datacenters"},
		{"/v1/catalog/nodes", " [" + node + "] ", "nodes"},
		{"/v1/catalog/services", ` {"z 雪": ["b", "a", "b"], "a":null, "empty":[]} `, "services"},
		{"/v1/catalog/service/svc%20%E9%9B%AA", "[" + instance + "]", "service"},
		{"/v1/catalog/node/node%20%E9%9B%AA", ` {"Node":` + node + `,"Services":{"s:原":` + service + `},"future":true} `, "node"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			r := catalogProjectionResult(t, c.path, c.body)
			assertCatalogOriginalReceipt(t, r, c.body)
			list := r.HTTP.Catalog
			if list == nil || list.Kind != c.kind || !list.ProjectionComplete || !list.Complete || list.Null || r.ProjectionError != "" || r.HTTP.Agent != nil || r.HTTP.Health != nil || r.HTTP.Sessions != nil {
				t.Fatal("Catalog native shape not projected independently", r.ProjectionError)
			}
			switch c.kind {
			case "datacenters":
				if !slices.Equal(list.Datacenters, []string{"dc2", "dc1"}) {
					t.Fatal("original datacenter ordering changed")
				}
			case "nodes":
				n := list.Nodes[0]
				if n.CreateIndex != math.MaxUint64 || n.ModifyIndex != 9007199254740993 || !bytes.Equal(n.Raw, []byte(node)) || !slices.Contains(n.NullFields, "Address") || slices.Contains(n.PresentFields, "Partition") || n.TaggedAddresses == nil {
					t.Fatal("Catalog node field precision/presence or raw changed", n)
				}
			case "services":
				if len(list.Services) != 3 || list.Services[0].Name != "z 雪" || !slices.Equal(list.Services[0].Tags, []string{"b", "a", "b"}) || !bytes.Equal(list.Services[0].Raw, []byte(`["b", "a", "b"]`)) || !list.Services[1].TagsNull || list.Services[1].Tags != nil || list.Services[2].Tags == nil || list.Services[2].TagsNull {
					t.Fatal("Catalog map/tag order or null/empty distinction changed")
				}
			case "service":
				i := list.Instances[0]
				if i.ServiceName != "svc 雪" || i.ServiceID != "s:原" || i.CreateIndex != math.MaxUint64 || i.ModifyIndex != 9007199254740993 || i.ServiceWeights.Passing != math.MaxUint64 || !slices.Contains(i.ServiceWeights.NullFields, "Warning") || !slices.Contains(i.NullFields, "Partition") || !slices.Contains(i.PresentFields, "Namespace") || !slices.Equal(i.ServiceTags, []string{"z", "a", "z"}) || i.ServiceTaggedAddresses["lan"].Port != 1234 || !bytes.Equal(i.Raw, []byte(instance)) {
					t.Fatal("flat Catalog instance lost original fields/precision/raw", i)
				}
			case "node":
				n := list.NodeServices
				if n == nil || n.Node.Node != "node 雪" || len(n.Services) != 1 || n.Services[0].Key != "s:原" || !bytes.Equal(n.Services[0].Raw, []byte(service)) || n.Services[0].Service.Weights.Passing != 3 || !bytes.Equal(n.Raw, []byte(c.body)) {
					t.Fatal("Catalog node lookup changed original keyed services")
				}
			}
			clone := r.Clone()
			if !reflect.DeepEqual(clone.HTTP.Catalog, list) {
				t.Fatal("Catalog clone changed presence")
			}
			mutateCatalogClone(clone.HTTP.Catalog)
			if reflect.DeepEqual(clone.HTTP.Catalog, list) {
				t.Fatal("Catalog clone fixture did not change")
			}
			reparsed := catalogProjectionResult(t, c.path, c.body)
			if !reflect.DeepEqual(list, reparsed.HTTP.Catalog) {
				t.Fatal("Catalog clone shares original slices/maps/raw")
			}
		})
	}
}

func mutateCatalogClone(l *contract.KVHTTPCatalogList) {
	if len(l.Datacenters) != 0 {
		l.Datacenters[0] = "changed"
	}
	if len(l.Nodes) != 0 {
		l.Nodes[0].Raw[0] = 'x'
		l.Nodes[0].Meta["original"] = "changed"
		l.Nodes[0].PresentFields[0] = "changed"
	}
	if len(l.Services) != 0 {
		l.Services[0].Tags[0] = "changed"
		l.Services[0].Raw[0] = 'x'
	}
	if len(l.Instances) != 0 {
		i := &l.Instances[0]
		i.Raw[0] = 'x'
		i.ServiceMeta["original"] = "changed"
		i.ServiceTags[0] = "changed"
		i.ServiceWeights.NullFields[0] = "changed"
		a := i.ServiceTaggedAddresses["lan"]
		a.PresentFields[0] = "changed"
		i.ServiceTaggedAddresses["lan"] = a
	}
	if l.NodeServices != nil {
		n := l.NodeServices
		n.Raw[0] = 'x'
		n.Node.Raw[0] = 'x'
		n.Node.Meta["original"] = "changed"
		n.Services[0].Raw[0] = 'x'
		n.Services[0].Service.Tags = append(n.Services[0].Service.Tags, "changed")
		n.Services[0].Service.Meta["original"] = "changed"
	}
}

func TestCatalogProjectionNullEmptyAndACLFilteredReceiptsKeepIndependentCompleteness(t *testing.T) {
	cases := []struct{ path, body string }{
		{"/v1/catalog/datacenters", "[]"}, {"/v1/catalog/nodes", "[]"}, {"/v1/catalog/services", "{}"}, {"/v1/catalog/service/svc", "[]"},
		{"/v1/catalog/node/node", " null "}, {"/v1/catalog/node/node", `{"Node":{"Node":"node"},"Services":{}}`},
		{"/v1/catalog/node/node", `{"Node":{"Node":"node"},"Services":null}`}, {"/v1/catalog/node/node", `{"Node":{"Node":"node"}}`},
	}
	for _, c := range cases {
		r := catalogProjectionResult(t, c.path, c.body)
		assertCatalogOriginalReceipt(t, r, c.body)
		if r.HTTP.Catalog == nil || !r.HTTP.Catalog.Complete || r.HTTP.Catalog.Null != (strings.TrimSpace(c.body) == "null") {
			t.Fatal("Catalog null/empty lookup changed native observation", c, r.ProjectionError)
		}
		r.HTTP.Metadata.ACLFiltered = new(true)
		projectDocumentCatalog(&r)
		if r.HTTP.Catalog == nil || !r.HTTP.Catalog.ProjectionComplete || r.HTTP.Catalog.Complete || !r.Completed || r.WriteOutcomeUnknown {
			t.Fatal("ACL-filtered Catalog receipt gained an absence or write claim")
		}
		for _, damage := range []func(*contract.KVHTTPResult){
			func(h *contract.KVHTTPResult) { h.Status = 403 }, func(h *contract.KVHTTPResult) { h.Captured = false }, func(h *contract.KVHTTPResult) { h.EOF = false },
			func(h *contract.KVHTTPResult) { h.Acknowledged = false }, func(h *contract.KVHTTPResult) { h.Succeeded = false }, func(h *contract.KVHTTPResult) { h.WriteOutcomeUnknown = true },
			func(h *contract.KVHTTPResult) { h.Request.Body = []byte{} }, func(h *contract.KVHTTPResult) { h.Request.Method = "PUT" },
		} {
			bad := r.Clone()
			bad.HTTP.Catalog = nil
			damage(bad.HTTP)
			projectDocumentCatalog(&bad)
			if bad.HTTP.Catalog != nil {
				t.Fatal("failed/body-carrying Catalog receipt promoted")
			}
		}
	}
}

func TestCatalogProjectionRejectsMalformedForeignDuplicateAndBoundedClaimsWithoutChangingACK(t *testing.T) {
	for path, bodies := range map[string][]string{
		"/v1/catalog/datacenters": {"null", `{}`, `[null]`, `["dc1","dc1"]`, `[] []`},
		"/v1/catalog/nodes":       {"null", `{}`, `[null]`, `[{"Node":null}]`, `[{"Node":"node","ModifyIndex":-1}]`, `[{"Node":"node","CreateIndex":18446744073709551616}]`, `[{"Node":"node","Meta":{"k":null}}]`, `[{"Node":"node"},{"Node":"node"}]`, `[{"Node":"node","Node":"node"}]`},
		"/v1/catalog/services":    {"null", `[]`, `{"svc":{}}`, `{"svc":[null]}`, `{"svc":[],"svc":[]}`, `{"\ud800":[]}`},
		"/v1/catalog/service/svc": {"null", `{}`, `[null]`, `[{"Node":"node","ServiceID":"s","ServiceName":"other"}]`, `[{"Node":"node","ServiceID":"s","ServiceName":"svc","ServicePort":65536}]`, `[{"Node":"node","ServiceID":"s","ServiceName":"svc","ServiceTaggedAddresses":{"lan":null}}]`, `[{"Node":"node","ServiceID":"s","ServiceName":"svc","ServiceWeights":{"Passing":-1}}]`, `[{"Node":"node","ServiceID":"s","ServiceName":"svc"},{"Node":"node","ServiceID":"s","ServiceName":"svc"}]`},
		"/v1/catalog/node/node":   {`[]`, `{}`, `{"Node":null,"Services":{}}`, `{"Node":{"Node":"other"},"Services":{}}`, `{"Node":{"Node":"node"},"Services":{"s":null}}`, `{"Node":{"Node":"node"},"Services":{"s":{"ID":"other","Service":"svc"}}}`, `{"Node":{"Node":"node"},"Services":{"s":{"ID":"s","Service":"svc","Port":1.5}}}`},
	} {
		for _, body := range bodies {
			r := catalogProjectionResult(t, path, body)
			assertCatalogOriginalReceipt(t, r, body)
			if r.HTTP.Catalog != nil || r.ProjectionError == "" {
				t.Fatal("malformed Catalog response projected", path, body)
			}
		}
	}
	// A foreign scope, an ignored option or a noncanonical escaped target cannot
	// acquire a Catalog projection through an otherwise acknowledged receipt.
	for _, request := range []contract.KVHTTPRequest{
		{Method: "GET", Path: "/v1/agent/services"}, {Method: "GET", Path: "/v1/catalog/services", Query: map[string][]string{"near": {"_agent"}}},
		{Method: "GET", Path: "/v1/catalog/node/%6eode"}, {Method: "GET", Path: "/v1/catalog/service/svc", Query: map[string][]string{"token": {"hidden"}}},
	} {
		r := catalogProjectionResult(t, "/v1/catalog/services", "{}")
		r.HTTP.Catalog = nil
		r.HTTP.Request = request
		projectDocumentCatalog(&r)
		if r.HTTP.Catalog != nil {
			t.Fatal("foreign or unsupported Catalog request promoted")
		}
	}
	var body strings.Builder
	body.WriteByte('{')
	for i := 0; i <= entryLimit; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"svc%d":[]`, i)
	}
	body.WriteByte('}')
	if _, err := decodeCatalogProjection([]byte(body.String()), catalogTarget{kind: "services"}); !errors.Is(err, ErrBounds) {
		t.Fatal("Catalog map truncated or confused with per-row field bound", err)
	}
	large := `[{"Node":"node","future":"` + strings.Repeat("x", projectionBudget) + `"}]`
	if _, err := decodeCatalogProjection([]byte(large), catalogTarget{kind: "nodes"}); !errors.Is(err, ErrBounds) {
		t.Fatal("Catalog raw values exceeded complete projection budget", err)
	}
	// Same local label in distinct native namespace/partition/peer presence stays
	// distinct. Neither field omission nor explicit empty/null is normalized.
	qualified := `[{"Node":"node","ServiceID":"s","ServiceName":"svc"},{"Node":"node","ServiceID":"s","ServiceName":"svc","Namespace":""},{"Node":"node","ServiceID":"s","ServiceName":"svc","Partition":null},{"Node":"node","ServiceID":"s","ServiceName":"svc","PeerName":"peer"}]`
	r := catalogProjectionResult(t, "/v1/catalog/service/svc", qualified)
	if r.HTTP.Catalog == nil || len(r.HTTP.Catalog.Instances) != 4 {
		t.Fatal("Catalog qualified native identities normalized", r.ProjectionError)
	}
}
