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
	"net/url"
	"reflect"
	"testing"
)

func nativeACLMutation(t *testing.T, c *Client, request Request, status int) Response {
	t.Helper()
	plan, err := c.Prepare(request)
	if err != nil || !plan.Writes() {
		t.Fatal("original private ACL preparation failed")
	}
	inspection := plan.Snapshot()
	if len(inspection.Body) != 0 {
		inspection.Body[0] ^= 1
	}
	r, err := c.Execute(context.Background(), plan)
	if r.Status != status || !r.Attempted || !r.Ended || !r.Captured || !r.EOF || !r.Acknowledged || r.WriteOutcomeUnknown || r.Succeeded != (status == 200) || (err == nil) != (status == 200) || !reflect.DeepEqual(r.Request, request) {
		t.Fatal("original native ACL mutation receipt changed", r.Status, status)
	}
	if _, err := c.Execute(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
		t.Fatal("original private ACL mutation replayed")
	}
	return r
}

func TestConsulNativeACLMutationOriginalDefinitionsCloneAndEndpointScopes(t *testing.T) {
	c, cfg, token := nativeClient(t)
	build := nativeCatalogRequest(t)
	if cfg.Auth != "token" {
		for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule} {
			body := []byte(`{}`)
			if resource == ACLReadAuthMethod {
				body = []byte(`{"Name":"owned-disabled"}`)
			}
			nativeACLMutation(t, c, build(ACLCreateRequest(resource, body)), 401)
		}
		return
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("owned validation key unavailable")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal("owned public key encoding failed")
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	prefix := "owned-" + t.Name()
	type item struct {
		resource ACLReadResource
		identity string
		alive    bool
	}
	var owned []*item
	t.Cleanup(func() {
		for i := len(owned) - 1; i >= 0; i-- {
			x := owned[i]
			if x.alive {
				nativeACLFixtureCall(t, c, "DELETE", "/v1/acl/"+string(x.resource)+"/"+url.PathEscape(x.identity), nil)
			}
		}
	})
	marshal := func(v any) []byte {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal("owned definition encoding failed")
		}
		return raw
	}
	var originalToken []byte
	for _, resource := range []ACLReadResource{ACLReadPolicy, ACLReadRole, ACLReadToken, ACLReadAuthMethod, ACLReadBindingRule} {
		body := map[string]any{"Description": "owned original 雪"}
		switch resource {
		case ACLReadPolicy:
			body["Name"], body["Rules"] = prefix+"-policy", `acl = "read"`
		case ACLReadRole:
			body["Name"], body["Policies"] = prefix+"-role", []map[string]string{{"ID": owned[0].identity}}
		case ACLReadToken:
			body["Roles"] = []map[string]string{{"ID": owned[1].identity}}
		case ACLReadAuthMethod:
			body["Name"], body["Type"] = prefix+"-method", "jwt"
			body["Config"] = map[string]any{"JWTValidationPubKeys": []string{public}, "BoundAudiences": []string{"owned-audience"}}
		case ACLReadBindingRule:
			body["AuthMethod"], body["BindType"], body["BindName"] = prefix+"-method", "service", "owned-service"
		}
		r := nativeACLMutation(t, c, build(ACLCreateRequest(resource, marshal(body))), 200)
		field := "ID"
		if resource == ACLReadToken {
			field, originalToken = "AccessorID", bytes.Clone(r.Body)
		} else if resource == ACLReadAuthMethod {
			field = "Name"
		}
		x := &item{resource: resource, identity: nativeACLString(t, r.Body, field), alive: true}
		owned = append(owned, x)
		body["Description"] = "owned updated 雪"
		if resource == ACLReadToken {
			delete(body, "Roles") // Native update replaces omitted links.
		}
		r = nativeACLMutation(t, c, build(ACLUpdateRequest(resource, x.identity, marshal(body))), 200)
		if resource == ACLReadToken {
			var value map[string]json.RawMessage
			if json.Unmarshal(r.Body, &value) != nil || len(value["Roles"]) != 0 || nativeACLString(t, r.Body, "SecretID") != nativeACLString(t, originalToken, "SecretID") {
				t.Fatal("native token omitted-secret/link semantics changed")
			}
			originalToken = bytes.Clone(r.Body)
		}
		configured := cfg
		configured.Datacenter = "dc1"
		client, err := New(configured, token)
		if err != nil {
			t.Fatal("original configured ACL client unavailable")
		}
		request := build(ACLUpdateRequest(resource, x.identity, marshal(body)))
		if resource == ACLReadToken || resource == ACLReadPolicy || resource == ACLReadRole {
			if _, err := client.Prepare(request); !errors.Is(err, ErrUnsupported) {
				t.Fatal("agent-local native ACL mutation accepted unobserved datacenter")
			}
			opened, evidence, err := Open(context.Background(), configured, token)
			if err != nil || !evidence.Identity.Available || evidence.Identity.Datacenter != "dc1" {
				t.Fatal("original ACL opening did not prove the selected datacenter")
			}
			nativeACLMutation(t, opened, request, 200)
			opened.Close()
		} else {
			r = nativeACLMutation(t, client, request, 200)
			u, err := url.Parse(r.URL)
			if err != nil || u.Query().Get("dc") != "dc1" {
				t.Fatal("routed ACL endpoint lost original datacenter")
			}
		}
		client.Close()
	}
	clone := nativeACLMutation(t, c, build(ACLTokenCloneRequest(owned[2].identity, new(""))), 200)
	cloneID := nativeACLString(t, clone.Body, "AccessorID")
	owned = append(owned, &item{resource: ACLReadToken, identity: cloneID, alive: true})
	if cloneID == owned[2].identity || nativeACLString(t, clone.Body, "SecretID") == nativeACLString(t, originalToken, "SecretID") || nativeACLString(t, clone.Body, "Description") != "owned updated 雪" {
		t.Fatal("native clone lost generated identities or empty-description inheritance")
	}
	for i := len(owned) - 1; i >= 0; i-- {
		x := owned[i]
		request := build(ACLDeleteRequest(x.resource, x.identity))
		nativeACLMutation(t, c, request, 200)
		x.alive = false
		nativeACLMutation(t, c, request, 404)
	}
}
