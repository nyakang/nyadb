package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const aclMutationTestID = "01234567-89ab-cdef-0123-456789abcdef"
const aclMutationOtherID = "fedcba98-7654-3210-fedc-ba9876543210"

func TestACLMutationRequestsPreserveOriginalBytesAndStayOutsideGenericDocuments(t *testing.T) {
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule} {
		identity := aclMutationTestID
		original := []byte(" {\n\"Description\":\"original 雪\\n\"\n} ")
		if resource == ACLReadAuthMethod {
			identity = "owned 雪%?#++"
			original = []byte(` {"Name":"owned 雪%?#++","Type":"jwt","Config":{"custom":{"Namespace":"config data","Token":"config data"}}} `)
		}
		for _, action := range []string{"create", "update", "delete"} {
			raw := bytes.Clone(original)
			var request Request
			var err error
			switch action {
			case "create":
				request, err = ACLCreateRequest(resource, raw)
			case "update":
				request, err = ACLUpdateRequest(resource, identity, raw)
			case "delete":
				request, err = ACLDeleteRequest(resource, identity)
			}
			if err != nil {
				t.Fatal(resource, action, err)
			}
			want := "/v1/acl/" + string(resource)
			if action != "create" {
				want += "/" + url.PathEscape(identity)
			}
			if request.Path != want || action == "delete" && (request.Method != "DELETE" || len(request.Body) != 0) || action != "delete" && (request.Method != "PUT" || !bytes.Equal(request.Body, original)) {
				t.Fatal("original ACL request changed", resource, action)
			}
			raw[0] = 'x'
			if action != "delete" && !bytes.Equal(request.Body, original) {
				t.Fatal("ACL request aliases authored bytes")
			}
			if _, err := DocumentRequest(request); !errors.Is(err, ErrUnsupported) {
				t.Fatal("private ACL mutation entered generic document", resource, action)
			}
		}
	}
	for _, description := range []*string{nil, new(""), new("original 雪\n")} {
		request, err := ACLTokenCloneRequest(aclMutationTestID, description)
		var fields map[string]string
		if err != nil || json.Unmarshal(request.Body, &fields) != nil || request.Path != "/v1/acl/token/"+aclMutationTestID+"/clone" {
			t.Fatal("original clone input changed", err)
		}
		value, present := fields["Description"]
		if present != (description != nil) || description != nil && value != *description {
			t.Fatal("clone description omission/empty/value changed")
		}
	}
}

func TestACLMutationRejectsAmbiguousIdentityScopeAndRedactedInputs(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"Description":"x","Description":"y"}`, `{"Description":"x","description":"y"}`, `{"Namespace":""}`, `{"Partition":null}`, `{"Token":"override"}`, `{"AuthMethod":"issued"}`, `{"CreateIndex":1}`, `{"SecretID":"<hidden>"}`, `{"AccessorID":"bad"}`, `{"Description":"\ud800"}`} {
		if _, err := ACLCreateRequest(ACLReadToken, []byte(raw)); err == nil {
			t.Fatal("invalid or unsupported private token write accepted")
		}
	}
	for _, resource := range []ACLReadResource{ACLReadPolicy, ACLReadRole, ACLReadBindingRule} {
		if _, err := ACLCreateRequest(resource, []byte(`{"ID":"`+aclMutationTestID+`"}`)); err == nil {
			t.Fatal("native create accepted an ignored or mismatched ID")
		}
		if _, err := ACLUpdateRequest(resource, aclMutationTestID, []byte(`{"ID":"`+aclMutationOtherID+`"}`)); err == nil {
			t.Fatal("native update accepted a foreign ID")
		}
	}
	if _, err := ACLUpdateRequest(ACLReadAuthMethod, "original", []byte(`{"Name":"foreign"}`)); err == nil {
		t.Fatal("AuthMethod update changed original name")
	}
	if _, err := ACLUpdateRequest(ACLReadAuthMethod, "original", []byte(`{"Config":{"nested":["<hidden>"]}}`)); err == nil {
		t.Fatal("AuthMethod update replayed a redacted config value")
	}
	if _, err := ACLTokenCloneRequest(aclMutationTestID, new(string([]byte{0xff}))); err == nil {
		t.Fatal("clone normalized invalid Unicode")
	}
	for _, id := range []string{"", "self", "name/original", "..", "a/../b", "a//b", "/leading", "trailing/", strings.Repeat("a", 4097)} {
		if _, err := ACLDeleteRequest(ACLReadToken, id); err == nil {
			t.Fatal("token delete accepted an unqualified accessor")
		}
	}
	for _, request := range []Request{
		{Method: "PUT", Path: "/v1/acl/token/" + aclMutationTestID + "/clone", Body: []byte(`{"Policies":[]}`)},
		{Method: "DELETE", Path: "/v1/acl/token/" + aclMutationTestID, Body: []byte(`{}`)},
		{Method: "PUT", Path: "/v1/acl/templated-policy/name/builtin%2Fservice", Body: []byte(`{}`)},
		{Method: "PUT", Path: "/v1/acl/bootstrap", Body: []byte(`{}`)},
		{Method: "PUT", Path: "/v1/acl/token", Body: []byte(`{}`), Query: url.Values{"dc": {"foreign"}}},
		{Method: "PUT", Path: "/v1/acl/auth-method", Body: []byte(`{"Name":"original"}`), Query: url.Values{"token": {"override"}}},
	} {
		if _, _, _, err := validateRequest(request); err == nil {
			t.Fatal("unverified ACL mutation path or scope accepted")
		}
	}
}

func TestACLMutationOriginalClientScopePreparationAndSingleUse(t *testing.T) {
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule} {
		var calls atomic.Int32
		body := []byte(` {"Description":"original"} `)
		identity := aclMutationTestID
		response := `{"ID":"` + identity + `","CreateIndex":7,"ModifyIndex":8}`
		if resource == ACLReadToken {
			response = `{"AccessorID":"` + identity + `","SecretID":"owned-memory-only","CreateIndex":7,"ModifyIndex":8}`
		} else if resource == ACLReadAuthMethod {
			identity, response = "original", `{"Name":"original","Config":{},"CreateIndex":7,"ModifyIndex":8}`
		}
		local := resource == ACLReadToken || resource == ACLReadPolicy || resource == ACLReadRole
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Equal(raw, body) || r.URL.Query().Has("consistent") || r.URL.Query().Has("stale") || r.URL.Query().Has("dc") == local {
				t.Error("original ACL mutation bytes or endpoint scope changed")
			}
			if !local && r.URL.Query().Get("dc") != "original-dc" {
				t.Error("routed ACL mutation lost original configured datacenter")
			}
			io.WriteString(w, response)
		})
		c.config.Datacenter, c.config.Consistency = "original-dc", "consistent"
		request, err := ACLUpdateRequest(resource, identity, body)
		if err != nil {
			t.Fatal(err)
		}
		if local {
			if _, err := c.Prepare(request); !errors.Is(err, ErrUnsupported) || calls.Load() != 0 {
				t.Fatal("local ACL endpoint silently ignored configured DC")
			}
			c.config.Datacenter = ""
		}
		plan, err := c.Prepare(request)
		if err != nil || !plan.Writes() || calls.Load() != 0 {
			t.Fatal("ACL preparation performed I/O or lost write identity", err)
		}
		request.Body[0] = 'x'
		inspection := plan.Snapshot()
		inspection.Body[1] = 'x'
		result, err := c.Execute(context.Background(), plan)
		if err != nil || !result.Attempted || !result.Ended || !result.Acknowledged || !result.Succeeded || result.WriteOutcomeUnknown || !bytes.Equal(result.Request.Body, body) {
			t.Fatal("ACL original successful receipt changed", resource, err)
		}
		if _, err := c.Execute(context.Background(), plan); !errors.Is(err, ErrPlanUsed) || calls.Load() != 1 {
			t.Fatal("ACL preparation was replayed")
		}
	}
}

func TestACLMutationCompleteUnknownReceiptsKeepBarrierAfterReads(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{400, `processed upstream before relay replaced reply`}, {500, `processed upstream`}, {408, ``},
		{200, `null`}, {200, `{}`}, {200, `{"ID":"` + aclMutationOtherID + `"}`}, {200, `{"ID":"` + aclMutationTestID + `","ID":"` + aclMutationTestID + `"}`},
	} {
		t.Run(fmt.Sprint(tc.status)+"-"+fmt.Sprint(len(tc.body)), func(t *testing.T) {
			var writes atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, `{"ID":"`+aclMutationTestID+`","Name":"original"}`)
					return
				}
				writes.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			request, err := ACLUpdateRequest(ACLReadPolicy, aclMutationTestID, []byte(`{"Name":"original","Rules":""}`))
			if err != nil {
				t.Fatal(err)
			}
			r, err := c.Do(context.Background(), request)
			if err == nil || r.Acknowledged || r.Succeeded || !r.WriteOutcomeUnknown || !r.Captured || !r.EOF || string(r.Body) != tc.body || writes.Load() != 1 {
				t.Fatal("uncertain ACL original outcome was guessed or replayed")
			}
			original, exists := c.UnknownResponse()
			if !exists {
				t.Fatal("unknown ACL write receipt missing")
			}
			read, _ := ACLItemReadRequest(ACLReadPolicy, aclMutationTestID)
			if _, err := c.Do(context.Background(), read); err != nil {
				t.Fatal("explicit later ACL read failed", err)
			}
			later, exists := c.UnknownResponse()
			if !exists || !reflect.DeepEqual(original, later) {
				t.Fatal("later read replaced the original unknown ACL write")
			}
			if _, err := c.Prepare(request); !errors.Is(err, ErrWriteUnknown) || writes.Load() != 1 {
				t.Fatal("later read removed the write barrier")
			}
		})
	}
}

func TestACLMutationAcknowledgementRequiresNativeIdentityIndicesAndSecret(t *testing.T) {
	for _, tc := range []struct {
		action       string
		status       int
		body         string
		ack, success bool
	}{
		{"delete", 200, "true\n", true, true},
		{"delete", 200, "false", false, false},
		{"delete", 200, "", false, false},
		{"delete", 404, "native missing", true, false},
		{"update", 404, "ambiguous upstream result", false, false},
		{"update", 403, "native ACL refused", true, false},
		{"update", 401, "ACL disabled", true, false},
		{"update", 200, `{"AccessorID":"` + aclMutationTestID + `","SecretID":"owned","CreateIndex":7,"ModifyIndex":8}`, true, true},
		{"update", 200, `{"AccessorID":"` + aclMutationTestID + `","SecretID":"<hidden>","CreateIndex":7,"ModifyIndex":8}`, false, false},
		{"update", 200, `{"AccessorID":"` + aclMutationTestID + `","SecretID":"owned"}`, false, false},
		{"update", 200, `{"AccessorID":"` + aclMutationTestID + `","SecretID":"owned","CreateIndex":9,"ModifyIndex":8}`, false, false},
		{"clone", 200, `{"AccessorID":"` + aclMutationTestID + `","SecretID":"owned","CreateIndex":7,"ModifyIndex":8}`, false, false},
		{"clone", 200, `{"AccessorID":"` + aclMutationOtherID + `","SecretID":"owned-new","CreateIndex":7,"ModifyIndex":8}`, true, true},
	} {
		request, err := aclMutationRequest(ACLReadToken, tc.action, aclMutationTestID, []byte(`{}`))
		if tc.action == "delete" {
			request, err = ACLDeleteRequest(ACLReadToken, aclMutationTestID)
		}
		if err != nil {
			t.Fatal(err)
		}
		kind, _, _, _ := validateRequest(request)
		result := Response{Status: tc.status, Body: []byte(tc.body), Captured: true, EOF: true}
		err = projectACLMutationResponse(&result, Plan{request: request, kind: kind})
		if result.Acknowledged != tc.ack || result.Succeeded != tc.success || (err == nil) != tc.success || string(result.Body) != tc.body {
			t.Fatal("ACL native acknowledgement was inferred from an incomplete or mismatched receipt", tc.action, tc.status)
		}
	}
}

func TestACLMutationOpeningEvidenceAllowsOnlyMatchingOriginalAgentScope(t *testing.T) {
	for _, available := range []bool{true, false} {
		for _, datacenter := range []string{"", "dc1", "dc2"} {
			var calls atomic.Int32
			fixture := sessionHandler(&calls, func(w http.ResponseWriter, r *http.Request) {
				t.Error("ACL scope preparation dispatched a mutation")
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !available && r.URL.Path == "/v1/agent/self" {
					calls.Add(1)
					w.WriteHeader(403)
					return
				}
				fixture(w, r)
			}))
			t.Cleanup(server.Close)
			cfg := Config{Endpoint: server.URL, Datacenter: datacenter}
			client, evidence, err := Open(context.Background(), cfg, "")
			if err != nil || evidence.Identity.Available != available {
				t.Fatal("original ACL opening observation failed", err)
			}
			t.Cleanup(func() { client.Close() })
			evidence.Identity.Available, evidence.Identity.Datacenter = true, "dc2"
			request, err := ACLCreateRequest(ACLReadToken, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			plan, err := client.Prepare(request)
			allowed := datacenter == "" || available && datacenter == "dc1"
			if (err == nil) != allowed || calls.Load() != before || !allowed && !errors.Is(err, ErrUnsupported) || allowed && !plan.Writes() {
				t.Fatal("ACL preparation trusted foreign/unavailable or mutable opening evidence")
			}
			session, err := OpenNativeSession(context.Background(), 99, cfg, "")
			if err != nil {
				t.Fatal("original scoped session unavailable", err)
			}
			t.Cleanup(func() { session.Close() })
			lease, err := session.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { lease.Close() })
			original := lease.(*documentLease)
			original.ops.Lock()
			err = original.connect(context.Background())
			physical := original.client
			original.ops.Unlock()
			if err != nil || physical == nil {
				t.Fatal("original physical session identity changed", err)
			}
			before = calls.Load()
			_, err = physical.Prepare(request)
			if (err == nil) != allowed || calls.Load() != before || !allowed && !errors.Is(err, ErrUnsupported) {
				t.Fatal("physical lease lost its original agent scope proof")
			}
		}
	}
}
