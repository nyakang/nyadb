package consul

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestACLReadFoundationCannotPublishSecretsThroughGenericDocuments(t *testing.T) {
	for _, path := range []string{"/v1/acl/tokens", "/v1/acl/token/self", "/v1/acl/auth-method/owned", "/v1/acl/templated-policies"} {
		if _, err := DocumentRequest(Request{Method: "GET", Path: path}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("private ACL read published a generic document")
		}
		raw, err := json.Marshal(requestEnvelope{Method: "GET", Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := decodeDocumentRequest(contract.KVRequest{Method: HTTPRequestMethod, JSON: string(raw)}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("authored request file bypassed private ACL publication")
		}
	}
}

func TestACLReadsPreserveNativeResourcesScopeAndPurePreparation(t *testing.T) {
	paths := map[ACLReadResource]string{
		ACLReadToken: "/v1/acl/tokens", ACLReadPolicy: "/v1/acl/policies", ACLReadRole: "/v1/acl/roles",
		ACLReadAuthMethod: "/v1/acl/auth-methods", ACLReadBindingRule: "/v1/acl/binding-rules",
		ACLReadTemplatedPolicy: "/v1/acl/templated-policies",
	}
	client, err := New(Config{Endpoint: "http://example.invalid", Datacenter: "original-dc", Prefix: "original-prefix", Consistency: "consistent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for resource, path := range paths {
		r, err := ACLListReadRequest(resource)
		if err != nil || r.Path != path || r.Method != "GET" || r.Body != nil || r.Query != nil {
			t.Fatal("native ACL list route changed", resource, err)
		}
		plan, err := client.Prepare(r)
		if err != nil || plan.Writes() || plan.Snapshot().Path != path {
			t.Fatal("ACL read acquired write authority", resource, err)
		}
		actual, err := client.requestURL(plan)
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(actual)
		if resource == ACLReadTemplatedPolicy {
			if parsed.RawQuery != "" {
				t.Fatal("static templates inherited ignored DC or consistency options")
			}
		} else if parsed.Query().Get("dc") != "original-dc" || !parsed.Query().Has("consistent") {
			t.Fatal("ACL data read lost its original connection scope")
		}
		if strings.Contains(actual, "original-prefix") {
			t.Fatal("KV prefix changed ACL scope")
		}
	}
	if r := ACLTokenSelfReadRequest(); r.Path != "/v1/acl/token/self" {
		t.Fatal("token self was replaced by an accessor lookup")
	}
}

func TestACLReadOptionsPreserveMembershipPresenceAndRejectIgnoredSemantics(t *testing.T) {
	id := "01234567-89ab-cdef-0123-456789abcdef"
	index, wait, empty, name := uint64(math.MaxUint64), "30ms", "", "original name 雪+%"
	tokens, _ := ACLListReadRequest(ACLReadToken)
	tokens.Query = url.Values{"stale": {""}}
	before := tokens.Clone()
	r, err := WithACLReadOptions(tokens, ACLReadOptions{Index: &index, Wait: &wait, Policy: &id, Role: &id, AuthMethod: &empty, ServiceName: &name})
	if err != nil || r.Query.Get("index") != "18446744073709551615" || r.Query.Get("servicename") != name || !r.Query.Has("authmethod") || r.Query.Get("authmethod") != "" || !reflect.DeepEqual(tokens, before) {
		t.Fatal("ACL options changed precision, presence or caller-owned inputs", err)
	}
	r.Query["stale"][0] = "changed"
	if tokens.Query.Get("stale") != "" {
		t.Fatal("ACL options shared the caller's query storage")
	}
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy} {
		request, _ := ACLListReadRequest(resource)
		for _, key := range []string{"filter", "near", "token", "dc", "ns", "partition", "expanded"} {
			bad := request.Clone()
			bad.Query = url.Values{key: {""}}
			if _, _, _, err := validateRequest(bad); err == nil {
				t.Fatal("ACL list advertised ignored or foreign routing", resource, key)
			}
		}
	}
	template, _ := ACLListReadRequest(ACLReadTemplatedPolicy)
	for _, key := range []string{"index", "wait", "consistent", "stale"} {
		r := template.Clone()
		r.Query = url.Values{key: {""}}
		if _, _, _, err := validateRequest(r); err == nil {
			t.Fatal("static template accepted blocking or consistency semantics", key)
		}
	}
	roles, _ := ACLListReadRequest(ACLReadRole)
	if _, err := WithACLReadOptions(roles, ACLReadOptions{Policy: &id}); err != nil {
		t.Fatal("native role-policy membership was rejected", err)
	}
	if _, err := WithACLReadOptions(roles, ACLReadOptions{Role: &id}); err == nil {
		t.Fatal("role list accepted token-only membership")
	}
	detail, _ := ACLItemReadRequest(ACLReadToken, id)
	if r, err := WithACLReadOptions(detail, ACLReadOptions{Expanded: true}); err != nil || !r.Query.Has("expanded") || r.Query.Get("expanded") != "" {
		t.Fatal("native expanded presence missing", err)
	}
	bad := detail.Clone()
	bad.Query = url.Values{"expanded": {"false"}}
	if _, _, _, err := validateRequest(bad); err == nil {
		t.Fatal("false-looking text was misrepresented as native unexpanded read")
	}
	for _, bad := range []Request{
		{Method: "PUT", Path: "/v1/acl/token"}, {Method: "DELETE", Path: detail.Path},
		{Method: "GET", Path: tokens.Path, Body: []byte("hidden")},
		{Method: "GET", Path: tokens.Path, Query: url.Values{"role": {id, id}}},
		{Method: "GET", Path: tokens.Path, Query: url.Values{"policy": {"not-a-uuid"}}},
	} {
		if _, err := WithACLReadOptions(bad, ACLReadOptions{}); err == nil {
			t.Fatal("ACL read admitted mutation, hidden body or ambiguous membership")
		}
	}
}

func TestACLNamedReadsPreserveNamesThroughNativeHTTPDecodersAndRawReceipts(t *testing.T) {
	name := "original 雪 +%?/part"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("X-Consul-Token") != "synthetic-credential" {
			t.Error("original ACL read changed method or credential binding")
		}
		logical := ""
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/acl/policy/name/"):
			logical, _ = url.QueryUnescape(strings.TrimPrefix(r.URL.Path, "/v1/acl/policy/name/"))
		case strings.HasPrefix(r.URL.Path, "/v1/acl/role/name/"):
			logical = strings.TrimPrefix(r.URL.Path, "/v1/acl/role/name/")
		}
		if logical != name {
			t.Error("original ACL name changed after native decoding")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ModifyIndex":18446744073709551615,"SecretID":"synthetic-response","Unknown":{"Exact":18446744073709551615}}`))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Auth: "token"}, "synthetic-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, resource := range []ACLReadResource{ACLReadPolicy, ACLReadRole} {
		request, err := ACLNameReadRequest(resource, name)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(context.Background(), request)
		if err != nil || !response.Captured || !response.EOF || !response.Acknowledged || !response.Succeeded || response.WriteOutcomeUnknown || response.Request.Path != request.Path {
			t.Fatal("original read receipt lost capture or identity", err)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(response.Body, &fields) != nil || string(fields["ModifyIndex"]) != "18446744073709551615" {
			t.Fatal("complete native unsigned bytes changed")
		}
		clone := response.Clone()
		clone.Body[0] = '!'
		if response.Body[0] != '{' {
			t.Fatal("original ACL receipt shared clone storage")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("ACL read retried or replayed")
	}
	for _, invalid := range []string{"", "bad\nname", "a//b", "a/../b", "/leading", string([]byte{255})} {
		if _, err := ACLNameReadRequest(ACLReadPolicy, invalid); err == nil {
			t.Fatal("unsupported ACL name was cleaned or normalized")
		}
	}
}
