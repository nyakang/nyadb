package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeCreationUUID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("owned creation ID unavailable")
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

func nativeCreationMethodFields(t *testing.T, c *Client, name string) map[string]json.RawMessage {
	t.Helper()
	raw := nativeACLFixtureCall(t, c, "GET", "/v1/acl/auth-method/"+url.PathEscape(name), nil)
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("owned original auth configuration unavailable")
	}
	return fields
}

func TestConsulNativeACLCreationAllResourcesOriginalDependenciesAndAuthUpsert(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclTokenCreate(), aclPlanLimits())
		if err == nil || p != nil || o.Complete || o.Creation == nil || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("ACL-disabled creation gained authority")
		}
		return
	}
	f := nativeACLSeed(t, base)
	method := nativeCreationMethodFields(t, base, f.method)
	for _, resource := range []string{"token", "policy", "role", "auth-method", "binding-rule"} {
		t.Run(resource, func(t *testing.T) {
			body := map[string]any{"Description": "owned original planned creation"}
			field := "ID"
			deps := 0
			switch resource {
			case "token":
				field = "AccessorID"
				body["Policies"], body["Roles"] = []map[string]string{{"Name": strings.ToUpper(f.policyName)}}, []map[string]string{{"Name": f.roleName}}
				deps = 2
			case "policy":
				body["Name"], body["Rules"] = "owned-planned-new-policy", `acl = "read"`
			case "role":
				body["Name"], body["Policies"] = "owned-planned-new-role", []map[string]string{{"Name": f.policyName}}
				deps = 1
			case "auth-method":
				field = "Name"
				body["Name"], body["Type"], body["Config"] = "owned-planned-new-method", "jwt", method["Config"]
			case "binding-rule":
				body["AuthMethod"], body["BindType"], body["BindName"] = f.method, "service", "owned-planned-new-service"
				deps = 1
			}
			raw, _ := json.Marshal(body)
			input := contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "create", Definition: append(append([]byte(" \n"), raw...), []byte("\n ")...)}
			out := nativeACLPlannedWrite(t, l, input)
			id := nativeACLString(t, out.Receipt.HTTP.Body, field)
			t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/"+resource+"/"+url.PathEscape(id), nil) })
			if out.Impact.Preview.Creation == nil || !reflect.DeepEqual(*out.Impact.Preview.Creation, input) || len(out.Impact.Preview.Dependencies) != deps || out.Impact.Preview.UpsertIdentity != "" || !bytes.Equal(out.Receipt.HTTP.Request.Body, input.Definition) {
				t.Fatal("native planned creation lost original input/dependencies")
			}
			if resource == "token" && (out.Projection == nil || !out.Projection.ContainsSensitiveValues) {
				t.Fatal("native creation lost generated private token")
			}
		})
	}
	t.Run("chosen-token-ids", func(t *testing.T) {
		accessor, chosen := nativeCreationUUID(t), nativeCreationUUID(t)
		raw, _ := json.Marshal(map[string]string{"AccessorID": accessor, "SecretID": chosen})
		out := nativeACLPlannedWrite(t, l, createACLInput("token", string(raw)))
		t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+accessor, nil) })
		if nativeACLString(t, out.Receipt.HTTP.Body, "AccessorID") != accessor || nativeACLString(t, out.Receipt.HTTP.Body, "SecretID") != chosen {
			t.Fatal("native chosen creation IDs changed")
		}
	})
	t.Run("existing-auth-method-upsert", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"Name": f.method, "Type": "jwt", "Config": method["Config"], "Description": "owned existing method upsert"})
		out := nativeACLPlannedWrite(t, l, createACLInput("auth-method", string(raw)))
		if out.Impact.Preview.UpsertIdentity != f.method || len(out.Impact.Preview.Reads) != 4 || len(out.Impact.Preview.References) != 1 || out.Impact.Preview.Reads[0].Result.Projection.Rows[0].ConfigState != "absent" || out.Receipt.HTTP.Request.Path != "/v1/acl/auth-method" {
			t.Fatal("native same-name creation did not preserve complete upsert observations")
		}
		found := false
		for _, ref := range out.Impact.Preview.References {
			found = found || ref.Resource == "binding-rule" && ref.Identity == f.binding
		}
		if !found {
			t.Fatal("native existing AuthMethod lost original binding rule impact")
		}
		actual := nativeACLFixtureCall(t, base, "GET", "/v1/acl/auth-method/"+url.PathEscape(f.method), nil)
		if nativeACLString(t, actual, "Description") != "owned existing method upsert" {
			t.Fatal("native original create endpoint did not update existing method")
		}
	})
}

func TestConsulNativeACLCreationCollisionsAndChangedDependenciesNeverDispatch(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclTokenCreate(), aclPlanLimits())
		if err == nil || p != nil || o.Complete {
			t.Fatal("disabled creation accepted")
		}
		return
	}
	f := nativeACLSeed(t, base)
	for _, resource := range []string{"policy", "role"} {
		name := f.policyName
		if resource == "role" {
			name = f.roleName
		}
		raw, _ := json.Marshal(map[string]string{"Name": strings.ToUpper(name)})
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), createACLInput(resource, string(raw)), aclPlanLimits())
		if !errors.Is(err, ErrConflict) || p != nil || o.Complete || len(o.Reads) != 1 {
			t.Fatal("native case-insensitive name collision gained authority")
		}
	}
	for _, field := range []string{"AccessorID", "SecretID"} {
		for _, value := range []string{f.linked, f.linkedSecret} {
			raw, _ := json.Marshal(map[string]string{field: value})
			p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), createACLInput("token", string(raw)), aclPlanLimits())
			if !errors.Is(err, ErrConflict) || p != nil || o.Complete || len(o.Reads) != 1 {
				t.Fatal("native cross-domain token collision gained authority")
			}
		}
	}
	definition, _ := json.Marshal(map[string]any{"Roles": []map[string]string{{"ID": f.role}}})
	input := createACLInput("token", string(definition))
	request, err := aclNativeMutationRequest(input)
	if err != nil {
		t.Fatal("owned creation request unavailable")
	}
	relay, writes, _ := nativeACLMutationFault(t, cfg, secret, "success", request)
	_, l = nativeACLDocumentSession(t, relay, secret)
	p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil {
		t.Fatal("original dependency preview unavailable")
	}
	nativeACLFixtureCall(t, base, "PUT", "/v1/acl/policy/"+f.policy, map[string]string{"Name": f.policyName, "Rules": `acl = "write"`})
	out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
	if !errors.Is(err, ErrConflict) || out.Receipt.Attempted || out.Impact == nil || out.Impact.Revalidated || !out.Impact.Recheck.Complete || writes.Load() != 0 || preview.Reads[0].BodySHA256 != out.Impact.Recheck.Reads[0].BodySHA256 {
		t.Fatal("unchanged token inventory hid changed role-derived policy rules")
	}
	if len(preview.Dependencies) != 2 || !reflect.DeepEqual(preview.Dependencies, out.Impact.Recheck.Dependencies) {
		t.Fatal("native dependency fixture changed identity instead of contents")
	}
	limits := aclPlanLimits()
	limits.MaxReads = 1
	p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, limits)
	if !errors.Is(err, ErrBounds) || p != nil || o.Complete || len(o.Reads) != 1 || writes.Load() != 0 {
		t.Fatal("native creation read limit allowed incomplete authority")
	}
}

func TestConsulNativeACLCreationProcessedFaultsRetainOriginalUpsertAndGeneratedReceipts(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	if cfg.Auth != "token" {
		_, l := nativeACLDocumentSession(t, cfg, secret)
		p, o, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclTokenCreate(), aclPlanLimits())
		if err == nil || p != nil || o.Complete {
			t.Fatal("disabled creation accepted")
		}
		return
	}
	f := nativeACLSeed(t, base)
	method := nativeCreationMethodFields(t, base, f.method)
	for _, resource := range []string{"token", "auth-method"} {
		for _, mode := range []string{"success", "lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close"} {
			t.Run(resource+"/"+mode, func(t *testing.T) {
				body := map[string]any{"Description": "owned actually processed creation " + mode}
				if resource == "token" {
					body["Roles"] = []map[string]string{{"ID": f.role}}
				} else {
					body["Name"], body["Type"], body["Config"] = f.method, "jwt", method["Config"]
				}
				definition, _ := json.Marshal(body)
				input := createACLInput(resource, string(definition))
				request, err := aclNativeMutationRequest(input)
				if err != nil {
					t.Fatal("original native creation request unavailable")
				}
				relay, writes, processed := nativeACLMutationFault(t, cfg, secret, mode, request)
				s, l := nativeACLDocumentSession(t, relay, secret)
				p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
				if err != nil || p == nil || !preview.Complete {
					t.Fatal("original native creation preview unavailable")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r contract.KVHTTPACLMutationResult
					e error
				}
				done := make(chan completion, 1)
				go func() { r, e := l.ExecuteHTTPACLMutation(ctx, p); done <- completion{r, e} }()
				var original []byte
				select {
				case original = <-processed:
				case <-time.After(20 * time.Second):
					t.Fatal("native original creation was not processed")
				}
				identity := f.method
				if resource == "token" {
					identity = nativeACLString(t, original, "AccessorID")
					t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+identity, nil) })
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
					t.Fatal("original creation executor did not acknowledge cancellation")
				}
				if mode == "close" {
					wait(t, closed)
				}
				if (out.e != nil) != (mode != "success") || out.r.Receipt.WriteOutcomeUnknown != (mode != "success") || out.r.Impact == nil || !out.r.Impact.Revalidated || !reflect.DeepEqual(out.r.Impact.Preview, preview) || CompareACLImpact(preview, out.r.Impact.Recheck) != nil || writes.Load() != 1 || !reflect.DeepEqual(out.r, l.LastHTTPACLMutation()) {
					t.Fatal("processed native creation lost original receipt or revalidation")
				}
				if mode == "partial" && (!bytes.Equal(out.r.Receipt.HTTP.Body, original[:len(original)/2]) || out.r.Receipt.HTTP.EOF || out.r.Receipt.HTTP.Captured) {
					t.Fatal("original partial creation receipt boundary changed")
				}
				actual := nativeACLFixtureCall(t, base, "GET", "/v1/acl/"+resource+"/"+url.PathEscape(identity), nil)
				if nativeACLString(t, actual, "Description") != body["Description"] {
					t.Fatal("fault did not follow native original creation")
				}
				recovery, err := s.RecoverKV(l)
				if err != nil {
					t.Fatal("pure original creation recovery failed")
				}
				defer recovery.Lease.Close()
				if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, out.r) || writes.Load() != 1 || recovery.Last.HTTP != nil || recovery.Unknown != nil {
					t.Fatal("native creation recovery replaced or replayed evidence")
				}
				if mode != "success" {
					if _, _, err := recovery.Lease.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits()); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("original unknown creation barrier lost")
					}
				}
			})
		}
	}
}
