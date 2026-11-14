package consul

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Only test fixture setup uses raw ACL writes. Never log their bodies, headers,
// generated credentials or full errors; the production foundation is read-only.
func nativeACLFixtureCall(t *testing.T, c *Client, method, path string, value any) []byte {
	t.Helper()
	var body []byte
	var err error
	if value != nil {
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal("owned ACL fixture encoding failed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.config.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal("owned ACL fixture request failed")
	}
	request.Header.Set("X-Consul-Token", c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		t.Fatal("owned ACL fixture transport failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK || !json.Valid(raw) {
		t.Fatal("owned ACL fixture receipt failed", response.StatusCode)
	}
	return raw
}

func nativeACLString(t *testing.T, raw []byte, field string) string {
	t.Helper()
	var row map[string]json.RawMessage
	var value string
	if json.Unmarshal(raw, &row) != nil || json.Unmarshal(row[field], &value) != nil || value == "" {
		t.Fatal("original ACL field missing", field)
	}
	return value
}

type nativeACLFixtures struct {
	policy, role, binding, method string
	policyName, roleName          string
	accessor, readonly, linked    string
	linkedSecret, denied          string
}

func nativeACLSeed(t *testing.T, c *Client) nativeACLFixtures {
	t.Helper()
	prefix := "owned-" + t.Name()
	create := func(resource, name, identityField string, value any) (string, []byte) {
		raw := nativeACLFixtureCall(t, c, "PUT", "/v1/acl/"+resource, value)
		id := nativeACLString(t, raw, identityField)
		t.Cleanup(func() { nativeACLFixtureCall(t, c, "DELETE", "/v1/acl/"+resource+"/"+url.PathEscape(id), nil) })
		return id, raw
	}
	f := nativeACLFixtures{policyName: prefix + "-policy", roleName: prefix + "-role"}
	f.policy, _ = create("policy", f.policyName, "ID", map[string]any{"Name": f.policyName, "Rules": `acl = "read"`})
	f.role, _ = create("role", f.roleName, "ID", map[string]any{"Name": f.roleName, "Policies": []map[string]string{{"ID": f.policy}}})
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("owned public validation key unavailable")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal("owned public validation key encoding failed")
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	f.method, _ = create("auth-method", prefix+"-method", "Name", map[string]any{
		"Name": prefix + "-method", "Type": "jwt", "Description": "owned original config 雪",
		"Config": map[string]any{"JWTValidationPubKeys": []string{public}, "BoundAudiences": []string{"owned-audience"}},
	})
	f.binding, _ = create("binding-rule", "", "ID", map[string]any{"AuthMethod": f.method, "Description": "owned original binding 雪", "BindType": "service", "BindName": "owned-acl-service"})
	var token []byte
	f.accessor, token = create("token", "", "AccessorID", map[string]any{"Description": "owned reader 雪", "Policies": []map[string]string{{"ID": f.policy}}})
	f.readonly = nativeACLString(t, token, "SecretID")
	f.linked, token = create("token", "", "AccessorID", map[string]any{"Description": "owned linked", "Roles": []map[string]string{{"ID": f.role}}, "ServiceIdentities": []map[string]string{{"ServiceName": "owned-acl-service"}}})
	f.linkedSecret = nativeACLString(t, token, "SecretID")
	_, token = create("token", "", "AccessorID", map[string]any{"Description": "owned denied", "Policies": []any{}})
	f.denied = nativeACLString(t, token, "SecretID")
	return f
}

func nativeACLRead(t *testing.T, c *Client, request Request, status int) Response {
	t.Helper()
	response, err := c.Do(context.Background(), request)
	if response.Status != status || !response.Attempted || !response.Ended || !response.Captured || !response.EOF || !response.Acknowledged || response.Succeeded != (status == 200) || response.WriteOutcomeUnknown || (err == nil) != (status == 200) {
		t.Fatal("original ACL receipt classification changed", response.Status, status)
	}
	if err != nil && (strings.Contains(err.Error(), c.token) && c.token != "") {
		t.Fatal("ACL error exposed original credential")
	}
	if !reflect.DeepEqual(response.Request, request) || !bytes.Equal(c.LastResponse().Body, response.Body) {
		t.Fatal("original ACL request or complete cached bytes changed")
	}
	return response
}

func TestConsulNativeACLReadResourcesMembershipAndOriginalScope(t *testing.T) {
	c, cfg, token := nativeClient(t)
	build := nativeCatalogRequest(t)
	resources := []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy}
	if cfg.Auth != "token" {
		for _, resource := range resources {
			nativeACLRead(t, c, build(ACLListReadRequest(resource)), http.StatusUnauthorized)
		}
		nativeACLRead(t, c, ACLTokenSelfReadRequest(), http.StatusUnauthorized)
		return
	}
	f := nativeACLSeed(t, c)
	cfg.Datacenter, cfg.Prefix, cfg.Consistency = "dc1", "unrelated-kv-prefix/", "consistent"
	reader, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	for _, resource := range resources {
		request := build(ACLListReadRequest(resource))
		if resource != ACLReadTemplatedPolicy {
			index, wait := uint64(0), "1ms"
			request = build(WithACLReadOptions(request, ACLReadOptions{Index: &index, Wait: &wait}))
		}
		response := nativeACLRead(t, reader, request, http.StatusOK)
		actual, err := url.Parse(response.URL)
		if err != nil {
			t.Fatal("original ACL URL unavailable")
		}
		if resource == ACLReadTemplatedPolicy {
			var templates map[string]json.RawMessage
			if json.Unmarshal(response.Body, &templates) != nil || len(templates) == 0 || templates["builtin/service"] == nil || actual.RawQuery != "" {
				t.Fatal("native static templates lost original map or inherited ignored scope")
			}
		} else {
			var rows []json.RawMessage
			if json.Unmarshal(response.Body, &rows) != nil || len(rows) == 0 || actual.Query().Get("dc") != "dc1" || actual.Query().Get("index") != "0" || !actual.Query().Has("consistent") || actual.Query().Get("wait") != "1ms" {
				t.Fatal("native ACL list shape, presence or original scope changed", resource)
			}
		}
		original := bytes.Clone(response.Body)
		response.Body[0] ^= 1
		if !bytes.Equal(reader.LastResponse().Body, original) {
			t.Fatal("ACL response clone mutated retained evidence")
		}
	}
	for _, item := range []struct {
		resource  ACLReadResource
		options   ACLReadOptions
		field, id string
	}{
		{ACLReadToken, ACLReadOptions{Policy: &f.policy}, "AccessorID", f.accessor},
		{ACLReadToken, ACLReadOptions{Role: &f.role}, "AccessorID", f.linked},
		{ACLReadToken, ACLReadOptions{ServiceName: str("owned-acl-service")}, "AccessorID", f.linked},
		{ACLReadRole, ACLReadOptions{Policy: &f.policy}, "ID", f.role},
		{ACLReadBindingRule, ACLReadOptions{AuthMethod: &f.method}, "ID", f.binding},
	} {
		request := build(WithACLReadOptions(build(ACLListReadRequest(item.resource)), item.options))
		response := nativeACLRead(t, reader, request, http.StatusOK)
		var rows []json.RawMessage
		if json.Unmarshal(response.Body, &rows) != nil || len(rows) != 1 || nativeACLString(t, rows[0], item.field) != item.id {
			t.Fatal("native ACL membership changed captured resource identities", item.resource)
		}
	}
	for _, item := range []struct {
		resource  ACLReadResource
		id, field string
	}{
		{ACLReadPolicy, f.policy, "ID"}, {ACLReadRole, f.role, "ID"}, {ACLReadAuthMethod, f.method, "Name"}, {ACLReadBindingRule, f.binding, "ID"}, {ACLReadTemplatedPolicy, "builtin/service", "TemplateName"},
	} {
		response := nativeACLRead(t, reader, build(ACLItemReadRequest(item.resource, item.id)), http.StatusOK)
		if nativeACLString(t, response.Body, item.field) != item.id {
			t.Fatal("ACL detail changed original identity", item.resource)
		}
	}
	for _, item := range []struct {
		resource ACLReadResource
		id, name string
	}{{ACLReadPolicy, f.policy, f.policyName}, {ACLReadRole, f.role, f.roleName}} {
		response := nativeACLRead(t, reader, build(ACLNameReadRequest(item.resource, item.name)), http.StatusOK)
		if nativeACLString(t, response.Body, "ID") != item.id || nativeACLString(t, response.Body, "Name") != item.name {
			t.Fatal("native ACL name lookup changed identity")
		}
	}
	index, wait := uint64(math.MaxUint64), "30ms"
	request := build(WithACLReadOptions(build(ACLListReadRequest(ACLReadRole)), ACLReadOptions{Index: &index, Wait: &wait}))
	start := time.Now()
	response := nativeACLRead(t, reader, request, http.StatusOK)
	actual, _ := url.Parse(response.URL)
	if time.Since(start) < 20*time.Millisecond || actual.Query().Get("index") != strconv.FormatUint(index, 10) {
		t.Fatal("native blocking ACL read lost wait or unsigned index")
	}
}

func TestConsulNativeACLReadOriginalCredentialRedactionDenialAndAbsence(t *testing.T) {
	c, cfg, _ := nativeClient(t)
	build := nativeCatalogRequest(t)
	if cfg.Auth != "token" {
		nativeACLRead(t, c, ACLTokenSelfReadRequest(), http.StatusUnauthorized)
		return
	}
	f := nativeACLSeed(t, c)
	reader, err := New(cfg, f.readonly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	self := nativeACLRead(t, reader, ACLTokenSelfReadRequest(), http.StatusOK)
	if nativeACLString(t, self.Body, "AccessorID") != f.accessor || nativeACLString(t, self.Body, "SecretID") != f.readonly {
		t.Fatal("token self lost original configured credential identity")
	}
	selfBytes := bytes.Clone(self.Body)
	detail := nativeACLRead(t, reader, build(ACLItemReadRequest(ACLReadToken, f.accessor)), http.StatusOK)
	if nativeACLString(t, detail.Body, "SecretID") != "<hidden>" || nativeACLString(t, detail.Body, "AccessorID") != f.accessor || !bytes.Equal(self.Body, selfBytes) {
		t.Fatal("redacted accessor detail replaced original complete self receipt")
	}
	for _, original := range []*Client{c, reader} {
		list := nativeACLRead(t, original, build(ACLListReadRequest(ACLReadToken)), http.StatusOK)
		var rows []json.RawMessage
		if json.Unmarshal(list.Body, &rows) != nil {
			t.Fatal("native token list malformed")
		}
		found := false
		for _, raw := range rows {
			if nativeACLString(t, raw, "AccessorID") != f.accessor {
				continue
			}
			found = true
			want := f.readonly
			if original == reader {
				want = "<hidden>"
			}
			if nativeACLString(t, raw, "SecretID") != want {
				t.Fatal("native token list secret/redaction presence changed")
			}
		}
		if !found {
			t.Fatal("original token missing from complete fixture list")
		}
	}
	expanded := nativeACLRead(t, c, build(WithACLReadOptions(build(ACLItemReadRequest(ACLReadToken, f.linked)), ACLReadOptions{Expanded: true})), http.StatusOK)
	var fields map[string]json.RawMessage
	if json.Unmarshal(expanded.Body, &fields) != nil || fields["ExpandedPolicies"] == nil || fields["ExpandedRoles"] == nil {
		t.Fatal("original expanded token receipt fields discarded")
	}
	denied, err := New(cfg, f.denied)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { denied.Close() })
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy} {
		nativeACLRead(t, denied, build(ACLListReadRequest(resource)), http.StatusForbidden)
	}
	missing := "01234567-89ab-cdef-0123-456789abcdef"
	for _, resource := range []ACLReadResource{ACLReadPolicy, ACLReadRole, ACLReadBindingRule, ACLReadAuthMethod, ACLReadToken} {
		status := http.StatusNotFound
		if resource == ACLReadToken {
			status = http.StatusForbidden
		}
		nativeACLRead(t, reader, build(ACLItemReadRequest(resource, missing)), status)
	}
	if !bytes.Equal(self.Body, selfBytes) {
		t.Fatal("later denied or missing read replaced original credential receipt")
	}
}

func TestConsulNativeACLReadFaultsCancellationAndRetirementNeverRetry(t *testing.T) {
	_, cfg, token := nativeClient(t)
	if cfg.Auth != "token" {
		c, err := New(cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		nativeACLRead(t, c, ACLTokenSelfReadRequest(), http.StatusUnauthorized)
		return
	}
	for _, mode := range []string{"lost", "partial", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			relayMode := mode
			if relayMode == "close" {
				relayMode = "cancel"
			}
			proxy, calls, observed := nativeAgentReadRelay(t, cfg, token, relayMode)
			reader, err := New(proxy, token)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			request := ACLTokenSelfReadRequest()
			plan, err := reader.Prepare(request)
			if err != nil || plan.Writes() || calls.Load() != 0 {
				t.Fatal("ACL preparation sent a request or acquired write authority")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				response Response
				err      error
			}
			done := make(chan completion, 1)
			go func() { response, err := reader.Execute(ctx, plan); done <- completion{response, err} }()
			select {
			case <-observed:
			case <-time.After(30 * time.Second):
				t.Fatal("original upstream ACL read did not complete")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				reader.Close()
			}
			select {
			case out := <-done:
				if out.err == nil || !out.response.Ended || out.response.Acknowledged || out.response.WriteOutcomeUnknown || calls.Load() != 1 {
					t.Fatal("damaged original ACL read promoted or retried")
				}
				if mode == "partial" && len(out.response.Body) == 0 || mode == "cancel" && !out.response.Canceled {
					t.Fatal("original partial or cancellation evidence discarded")
				}
				if mode != "close" && !bytes.Equal(reader.LastResponse().Body, out.response.Body) {
					t.Fatal("partial ACL evidence changed in cache")
				}
				if mode == "close" && len(reader.LastResponse().Body) != 0 {
					t.Fatal("closed ACL client retained its private receipt cache")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ACL cancellation did not wait for original executor")
			}
			if _, err := reader.Execute(context.Background(), plan); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
				t.Fatal("retired ACL plan replayed")
			}
		})
	}
}
