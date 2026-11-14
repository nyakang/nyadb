package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeACLPlannedWrite(t *testing.T, l *documentLease, input contract.KVHTTPACLMutationRequest) contract.KVHTTPACLMutationResult {
	t.Helper()
	p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil || p == nil || !preview.Complete {
		t.Fatal("original native mutation preview unavailable")
	}
	out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
	if err != nil || !out.Receipt.Completed || out.Receipt.WriteOutcomeUnknown || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || CompareACLImpact(out.Impact.Preview, out.Impact.Recheck) != nil || !reflect.DeepEqual(out, l.LastHTTPACLMutation()) {
		t.Fatal("original revalidated native mutation failed")
	}
	if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original native plan replayed")
	}
	if l.s.reserved != 0 || l.planReserved != 0 || p.Impact().Complete {
		t.Fatal("native planned mutation leaked reservation")
	}
	return out
}

func TestConsulNativeACLMutationPlanOriginalResourceWritesAndClone(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
		if err == nil || p != nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("disabled ACL plan gained write authority")
		}
		return
	}
	f := nativeACLSeed(t, base)
	method := nativeACLFixtureCall(t, base, "GET", "/v1/acl/auth-method/"+url.PathEscape(f.method), nil)
	var methodFields map[string]json.RawMessage
	if json.Unmarshal(method, &methodFields) != nil {
		t.Fatal("owned auth configuration unavailable")
	}
	for _, resource := range []string{"policy", "role", "token", "auth-method", "binding-rule"} {
		id, field := f.policy, "ID"
		body := map[string]any{"Description": "owned plan update"}
		switch resource {
		case "policy":
			body["Name"], body["Rules"] = f.policyName, `acl = "read"`
		case "role":
			id = f.role
			body["Name"], body["Policies"] = f.roleName, []map[string]string{{"ID": f.policy}}
		case "token":
			id, field = f.linked, "AccessorID"
			body["Roles"] = []map[string]string{{"ID": f.role}}
		case "auth-method":
			id, field = f.method, "Name"
			body["Name"], body["Type"], body["Config"] = f.method, "jwt", methodFields["Config"]
		case "binding-rule":
			id = f.binding
			body["AuthMethod"], body["BindType"], body["BindName"] = f.method, "service", "owned-planned-service"
		}
		t.Run(resource+"/update", func(t *testing.T) {
			raw, _ := json.Marshal(body)
			input := contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "update", Identity: id, Definition: append(append([]byte(" \n"), raw...), []byte("\n ")...)}
			out := nativeACLPlannedWrite(t, l, input)
			if nativeACLString(t, out.Receipt.HTTP.Body, field) != id || !bytes.Equal(out.Receipt.HTTP.Request.Body, input.Definition) {
				t.Fatal("original native update body or identity changed")
			}
		})
		t.Run(resource+"/delete", func(t *testing.T) {
			if resource == "policy" || resource == "role" || resource == "auth-method" {
				body["Name"] = "owned-plan-delete-" + resource
			}
			created := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/"+resource, body)
			owned := nativeACLString(t, created, field)
			alive := true
			t.Cleanup(func() {
				if alive {
					nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/"+resource+"/"+url.PathEscape(owned), nil)
				}
			})
			out := nativeACLPlannedWrite(t, l, contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "delete", Identity: owned})
			if !bytes.Equal(bytes.TrimSpace(out.Receipt.HTTP.Body), []byte("true")) {
				t.Fatal("native delete acknowledgement changed")
			}
			alive = false
		})
	}
	t.Run("token/clone", func(t *testing.T) {
		out := nativeACLPlannedWrite(t, l, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "clone", Identity: f.linked, Description: new("owned planned clone")})
		id := nativeACLString(t, out.Receipt.HTTP.Body, "AccessorID")
		t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+id, nil) })
		if id == f.linked || out.Projection == nil || !out.Projection.ContainsSensitiveValues {
			t.Fatal("planned clone lost newly generated private token")
		}
	})
}

func TestConsulNativeACLMutationPlanChangedReferenceBodiesStopOriginalWrite(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
		if err == nil || p != nil || o.Complete {
			t.Fatal("disabled plan accepted")
		}
		return
	}
	f := nativeACLSeed(t, base)
	other := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/policy", map[string]string{"Name": "owned-plan-other-policy", "Rules": `acl = "read"`})
	otherID := nativeACLString(t, other, "ID")
	alive := true
	t.Cleanup(func() {
		if alive {
			nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/policy/"+otherID, nil)
		}
	})
	nativeACLFixtureCall(t, base, "PUT", "/v1/acl/token/"+f.accessor, map[string]any{"Policies": []map[string]string{{"ID": f.policy}, {"ID": otherID}}})
	input := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: f.policy}
	request, err := aclNativeMutationRequest(input)
	if err != nil {
		t.Fatal("original native request unavailable")
	}
	relay, writes, _ := nativeACLMutationFault(t, cfg, secret, "success", request)
	_, l = nativeACLDocumentSession(t, relay, secret)
	p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil {
		t.Fatal("original native preview unavailable")
	}
	nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/policy/"+otherID, nil)
	alive = false
	out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
	if !errors.Is(err, ErrConflict) || out.Receipt.Attempted || out.Impact == nil || out.Impact.Revalidated || !out.Impact.Recheck.Complete || !reflect.DeepEqual(out.Impact.Preview, preview) || writes.Load() != 0 {
		t.Fatal("unchanged native indices bypassed original reference body comparison")
	}
	if !reflect.DeepEqual(preview.Reads[1].Result.Receipt.HTTP.Metadata.Index, out.Impact.Recheck.Reads[1].Result.Receipt.HTTP.Metadata.Index) {
		t.Fatal("fixture did not retain original token query index")
	}
	remaining := nativeACLFixtureCall(t, base, "GET", "/v1/acl/policy/"+f.policy, nil)
	if nativeACLString(t, remaining, "ID") != f.policy {
		t.Fatal("conflicted plan removed target")
	}
	if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("conflicted original plan replayed")
	}
}

func TestConsulNativeACLMutationPlanProcessedFaultsKeepOriginalPreviewAndRecheck(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	if cfg.Auth != "token" {
		_, l := nativeACLDocumentSession(t, cfg, secret)
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
		if err == nil || p != nil || o.Complete {
			t.Fatal("disabled plan accepted")
		}
		return
	}
	for _, mode := range []string{"success", "lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			name := "owned-plan-fault-" + mode
			created := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/policy", map[string]string{"Name": name, "Rules": `acl = "read"`})
			id := nativeACLString(t, created, "ID")
			t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/policy/"+id, nil) })
			definition, _ := json.Marshal(map[string]string{"Name": name, "Description": "owned original actually processed", "Rules": `acl = "read"`})
			input := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "update", Identity: id, Definition: definition}
			request, err := aclNativeMutationRequest(input)
			if err != nil {
				t.Fatal("original planned request unavailable")
			}
			relay, writes, processed := nativeACLMutationFault(t, cfg, secret, mode, request)
			s, l := nativeACLDocumentSession(t, relay, secret)
			p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
			if err != nil || !preview.Complete {
				t.Fatal("original planned fault preview unavailable")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r contract.KVHTTPACLMutationResult
				e error
			}
			done := make(chan completion, 1)
			go func() { r, e := l.ExecuteHTTPACLMutation(ctx, p); done <- completion{r, e} }()
			var body []byte
			select {
			case body = <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("native original planned mutation not processed")
			}
			closed := make(chan struct{})
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				go func() { p.Close(); close(closed) }()
			}
			var out completion
			select {
			case out = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("original planned executor did not settle")
			}
			if mode == "close" {
				wait(t, closed)
			}
			if (out.e != nil) != (mode != "success") || out.r.Receipt.WriteOutcomeUnknown != (mode != "success") || out.r.Impact == nil || !out.r.Impact.Revalidated || !reflect.DeepEqual(out.r.Impact.Preview, preview) || CompareACLImpact(preview, out.r.Impact.Recheck) != nil || writes.Load() != 1 || !reflect.DeepEqual(out.r, l.LastHTTPACLMutation()) {
				t.Fatal("processed original write lost known/unknown receipt and read evidence")
			}
			if mode == "partial" && (!bytes.Equal(out.r.Receipt.HTTP.Body, body[:len(body)/2]) || out.r.Receipt.HTTP.Captured || out.r.Receipt.HTTP.EOF) {
				t.Fatal("planned partial write lost original body boundary")
			}
			actual := nativeACLFixtureCall(t, base, "GET", "/v1/acl/policy/"+id, nil)
			if nativeACLString(t, actual, "Description") != "owned original actually processed" {
				t.Fatal("fault did not follow actual upstream mutation")
			}
			recovery, err := s.RecoverKV(l)
			if err != nil {
				t.Fatal("original native planned recovery failed")
			}
			defer recovery.Lease.Close()
			if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, out.r) || writes.Load() != 1 || recovery.Last.HTTP != nil || recovery.Unknown != nil {
				t.Fatal("native planned recovery changed original evidence or replayed write")
			}
			if mode != "success" {
				if _, _, err := recovery.Lease.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits()); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("native unknown planned write lost barrier")
				}
			}
		})
	}
}
