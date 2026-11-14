package consul

import (
	"bytes"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const aclProjectionID = "01234567-89ab-4cde-8123-456789abcdef"

func aclProjectionReceipt(t *testing.T, request contract.KVHTTPACLReadRequest, body string) contract.KVResult {
	t.Helper()
	native, err := aclNativeReadRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := documentRequestEnvelope(native)
	if err != nil {
		t.Fatal(err)
	}
	return documentResult(envelope, Response{Request: native, Kind: "acl-read", Status: 200, Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Body: []byte(body)})
}

func aclProjected(t *testing.T, request contract.KVHTTPACLReadRequest, body string) contract.KVHTTPACLReadResult {
	t.Helper()
	r := aclProjectionReceipt(t, request, body)
	original := r.Clone()
	out := projectACLReadResult(contract.KVScope{Generation: 1}, request, r)
	if !reflect.DeepEqual(original, out.Receipt) || !reflect.DeepEqual(original, r) {
		t.Fatal("ACL projection modified original receipt, ACK or raw bytes")
	}
	return out
}

func TestACLProjectionPreservesSixShapesOriginalOrderPresenceAndUnsignedValues(t *testing.T) {
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy} {
		t.Run(string(resource), func(t *testing.T) {
			identity := `"ID":"` + aclProjectionID + `","Name":"original"`
			wantIdentity := aclProjectionID
			lookup := "id"
			switch resource {
			case ACLReadToken:
				identity = `"AccessorID":"` + aclProjectionID + `","SecretID":"<hidden>"`
			case ACLReadAuthMethod:
				identity, wantIdentity, lookup = `"Name":"oidc-雪"`, "oidc-雪", "name"
			case ACLReadTemplatedPolicy:
				identity, wantIdentity, lookup = `"TemplateName":"builtin/service"`, "builtin/service", "name"
			}
			row := `{` + identity + `,"Description":null,"CreateIndex":18446744073709551615,"ModifyIndex":9007199254740993,"Future":{"unknown":[18446744073709551615]}}`
			body := " [ " + row + " ] "
			if resource == ACLReadTemplatedPolicy {
				body = ` {"builtin/service":` + row + `} `
			}
			for _, request := range []contract.KVHTTPACLReadRequest{{Resource: string(resource), Lookup: "list"}, {Resource: string(resource), Lookup: lookup, Identity: wantIdentity}} {
				raw := body
				if request.Lookup != "list" {
					raw = row
				}
				out := aclProjected(t, request, raw)
				p := out.Projection
				if p == nil || !p.ProjectionComplete || !p.Complete || p.Null || len(p.Rows) != 1 || out.ProjectionError != "" {
					t.Fatal("ACL original shape lost")
				}
				r := p.Rows[0]
				if r.Identity != wantIdentity || r.CreateIndex == nil || *r.CreateIndex != math.MaxUint64 || r.ModifyIndex == nil || *r.ModifyIndex != 9007199254740993 || !slices.Contains(r.NullFields, "Description") || slices.Contains(r.PresentFields, "Partition") || !bytes.Equal(r.Raw, []byte(row)) {
					t.Fatal("ACL original identity, precision, presence or raw changed")
				}
				if resource == ACLReadTemplatedPolicy && request.Lookup == "list" && r.Key != "builtin/service" {
					t.Fatal("native template key lost")
				}
			}
		})
	}
	for _, c := range []struct{ resource, body, first, second string }{
		{"templated-policy", `{"builtin/z":{"TemplateName":"builtin/z"},"builtin/a":{"TemplateName":"builtin/a"}}`, "builtin/z", "builtin/a"},
		{"role", `[{"ID":"` + aclProjectionID + `"},{"ID":"11111111-2222-4333-8444-555555555555"}]`, aclProjectionID, testAgentID},
	} {
		out := aclProjected(t, contract.KVHTTPACLReadRequest{Resource: c.resource, Lookup: "list"}, c.body)
		if out.Projection == nil || len(out.Projection.Rows) != 2 || out.Projection.Rows[0].Identity != c.first || out.Projection.Rows[1].Identity != c.second {
			t.Fatal("ACL native collection ordering changed")
		}
	}
}

func TestACLProjectionSecretPresenceAndNestedConfigValueRedaction(t *testing.T) {
	for _, c := range []struct{ field, state string }{{"", "absent"}, {`,"SecretID":null`, "null"}, {`,"SecretID":""`, "empty"}, {`,"SecretID":"<hidden>"`, "redacted"}, {`,"SecretID":"synthetic-test-value"`, "value"}} {
		out := aclProjected(t, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}, `{"AccessorID":"`+aclProjectionID+`"`+c.field+`}`)
		if p := out.Projection; p == nil || p.Rows[0].SecretState != c.state || p.ContainsRedaction != (c.state == "redacted") || p.ContainsSensitiveValues != (c.state == "value") {
			t.Fatal("native SecretID state changed", c.state)
		}
	}
	for _, c := range []struct{ field, state string }{
		{"", "absent"}, {`,"Config":null`, "null"}, {`,"Config":{}`, "empty"},
		{`,"Config":{"<hidden>":"not-redacted"}`, "value"},
		{`,"Config":{"nested":[{"<hidden>":false},18446744073709551615,null]}`, "value"},
		{`,"Config":{"nested":[{"secret":"<hidden>"}]}`, "redacted"},
		{`,"Config":{"nested":[null,false,["<hidden>"]],"other":"test-value"}`, "redacted"},
	} {
		out := aclProjected(t, contract.KVHTTPACLReadRequest{Resource: "auth-method", Lookup: "name", Identity: "oidc"}, `{"Name":"oidc"`+c.field+`}`)
		if p := out.Projection; p == nil || p.Rows[0].ConfigState != c.state || p.ContainsRedaction != (c.state == "redacted") || p.ContainsSensitiveValues != (c.state == "value" || c.state == "redacted") {
			t.Fatal("Config value redaction confused with key, or private values lost", c.state)
		}
	}
}

func TestACLProjectionRejectsMalformedDuplicateForeignAndBoundedClaimsKeepingReceipt(t *testing.T) {
	request := contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "list"}
	for _, body := range []string{
		`{}`, `[null]`, `[{"ID":null}]`, `[{"ID":"invalid"}]`, `[{"ID":"` + aclProjectionID + `","ID":"` + aclProjectionID + `"}]`,
		`[{"ID":"` + aclProjectionID + `"},{"ID":"` + aclProjectionID + `"}]`,
		`[{"ID":"` + aclProjectionID + `","ModifyIndex":18446744073709551616}]`,
		`[{"ID":"` + aclProjectionID + `","Name":"\ud800"}]`,
		`[{"ID":"` + aclProjectionID + `","Future":{"same":1,"same":2}}]`,
		`[{"ID":"` + aclProjectionID + `","Future":` + strings.Repeat("[", 66) + `0` + strings.Repeat("]", 66) + `}]`,
		`[{"ID":"` + aclProjectionID + `","Future":"` + strings.Repeat("x", projectionBudget) + `"}]`,
		strings.Repeat(" ", responseLimit+1),
	} {
		out := aclProjected(t, request, body)
		if out.Projection != nil || out.ProjectionError != "ACL projection unavailable; original receipt retained" {
			t.Fatal("malformed or bounded ACL result projected, or parser input exposed")
		}
	}
	for _, c := range []struct {
		request contract.KVHTTPACLReadRequest
		body    string
	}{
		{contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "id", Identity: testAgentID}, `{"AccessorID":"` + aclProjectionID + `"}`},
		{contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "name", Identity: "original"}, `{"ID":"` + aclProjectionID + `","Name":"foreign"}`},
		{contract.KVHTTPACLReadRequest{Resource: "templated-policy", Lookup: "list"}, `{"builtin/service":{"TemplateName":"builtin/node"}}`},
		{contract.KVHTTPACLReadRequest{Resource: "auth-method", Lookup: "list"}, `[{"Name":"oidc","Config":[]}]`},
	} {
		if out := aclProjected(t, c.request, c.body); out.Projection != nil || out.ProjectionError == "" {
			t.Fatal("foreign ACL identity or invalid shape projected")
		}
	}
	for _, mutate := range []func(*contract.KVHTTPResult){
		func(h *contract.KVHTTPResult) { h.Request.Method = "PUT" },
		func(h *contract.KVHTTPResult) { h.Request.Path = "/v1/acl/roles" },
		func(h *contract.KVHTTPResult) { h.Request.Query = map[string][]string{"index": {"1"}} },
		func(h *contract.KVHTTPResult) { h.Request.Body = []byte("foreign") },
	} {
		r := aclProjectionReceipt(t, request, `[]`)
		mutate(r.HTTP)
		original := r.Clone()
		out := projectACLReadResult(contract.KVScope{}, request, r)
		if out.Projection != nil || out.ProjectionError == "" || !reflect.DeepEqual(out.Receipt, original) {
			t.Fatal("foreign original request projected or rewritten")
		}
	}
}

func TestACLProjectionKeepsNullEmptyFilteredAndDeniedObservationsDistinct(t *testing.T) {
	request := contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "list"}
	for _, body := range []string{"null", "[]"} {
		out := aclProjected(t, request, body)
		if p := out.Projection; p == nil || !p.ProjectionComplete || p.Null != (body == "null") || p.Complete != (body != "null") || len(p.Rows) != 0 {
			t.Fatal("native null became complete empty ACL inventory")
		}
	}
	for _, mutate := range []func(*contract.KVResult){
		func(r *contract.KVResult) { r.HTTP.Metadata.ACLFiltered = new(true) },
		func(r *contract.KVResult) { r.HTTP.ProjectionError = "metadata unavailable" },
	} {
		r := aclProjectionReceipt(t, request, `[]`)
		mutate(&r)
		out := projectACLReadResult(contract.KVScope{}, request, r)
		if out.Projection == nil || !out.Projection.ProjectionComplete || out.Projection.Complete {
			t.Fatal("filtered or incomplete metadata claimed complete ACL inventory")
		}
	}
	for _, mutate := range []func(*contract.KVResult){
		func(r *contract.KVResult) { r.HTTP.Status = 403 },
		func(r *contract.KVResult) { r.HTTP.Status = 404 },
		func(r *contract.KVResult) { r.Completed = false },
		func(r *contract.KVResult) { r.HTTP.EOF = false },
		func(r *contract.KVResult) { r.HTTP.Captured = false },
		func(r *contract.KVResult) { r.HTTP.Acknowledged = false },
		func(r *contract.KVResult) { r.HTTP.Succeeded = false },
	} {
		r := aclProjectionReceipt(t, request, `[]`)
		mutate(&r)
		original := r.Clone()
		out := projectACLReadResult(contract.KVScope{}, request, r)
		if out.Projection != nil || !reflect.DeepEqual(out.Receipt, original) {
			t.Fatal("denial, absence or incomplete read became ACL inventory")
		}
	}
}
