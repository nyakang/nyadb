package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const creationSecret = "22222222-3333-4444-8555-000000000001"

type aclCreationFixture struct {
	l                                         *documentLease
	reads, writes                             atomic.Int32
	changed, unknown, filtered, missingSecret atomic.Bool
	mu                                        sync.Mutex
	body                                      []byte
}

func newACLCreationFixture(t *testing.T) *aclCreationFixture {
	t.Helper()
	f := &aclCreationFixture{}
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			f.writes.Add(1)
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.body = bytes.Clone(raw)
			f.mu.Unlock()
			if f.unknown.Load() {
				w.WriteHeader(500)
				io.WriteString(w, "original processed creation unavailable")
				return
			}
			key, value := "ID", aclMutationOtherID
			resource := strings.TrimPrefix(r.URL.Path, "/v1/acl/")
			if resource == "token" {
				key = "AccessorID"
			}
			if resource == "auth-method" {
				key = "Name"
				var fields map[string]string
				json.Unmarshal(raw, &fields)
				value = fields["Name"]
			}
			ack := map[string]any{key: value, "CreateIndex": 7, "ModifyIndex": 9}
			if resource == "token" {
				ack["SecretID"] = "generated-synthetic-secret"
			}
			json.NewEncoder(w).Encode(ack)
			return
		}
		f.reads.Add(1)
		if f.filtered.Load() {
			w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
		}
		policy := map[string]any{"ID": impactPolicy, "Name": "OriginalPolicy", "ModifyIndex": 8, "Rules": "original rules"}
		if f.changed.Load() {
			policy["Rules"] = "changed rules"
		}
		role := map[string]any{"ID": impactRole, "Name": "OriginalRole", "ModifyIndex": 8, "Policies": []map[string]string{{"ID": impactPolicy}}}
		method := map[string]any{"Name": "OriginalAuth", "Type": "jwt", "ModifyIndex": 8, "Config": map[string]string{"Synthetic": "original configuration"}}
		if f.changed.Load() {
			method["Config"] = map[string]string{"Synthetic": "changed configuration"}
		}
		token := map[string]any{"AccessorID": impactDirect, "SecretID": creationSecret, "ModifyIndex": 8}
		if f.missingSecret.Load() {
			delete(token, "SecretID")
		}
		switch r.URL.Path {
		case "/v1/acl/tokens":
			if r.URL.Query().Get("authmethod") == "OriginalAuth" {
				token["AuthMethod"], token["Local"] = "OriginalAuth", true
			}
			json.NewEncoder(w).Encode([]any{token})
		case "/v1/acl/policies":
			json.NewEncoder(w).Encode([]any{map[string]any{"ID": impactPolicy, "Name": "OriginalPolicy", "ModifyIndex": 8}})
		case "/v1/acl/roles":
			json.NewEncoder(w).Encode([]any{role})
		case "/v1/acl/auth-methods":
			json.NewEncoder(w).Encode([]any{map[string]any{"Name": "OriginalAuth", "Type": "jwt", "ModifyIndex": 8}})
		case "/v1/acl/binding-rules":
			json.NewEncoder(w).Encode([]any{map[string]any{"ID": impactBoth, "AuthMethod": "OriginalAuth", "ModifyIndex": 8}})
		case "/v1/acl/policy/" + impactPolicy:
			json.NewEncoder(w).Encode(policy)
		case "/v1/acl/role/" + impactRole:
			json.NewEncoder(w).Encode(role)
		case "/v1/acl/auth-method/OriginalAuth":
			json.NewEncoder(w).Encode(method)
		default:
			w.WriteHeader(404)
			io.WriteString(w, "original dependency missing")
		}
	})
	f.l = aclDocumentLease(t, s, 1)
	return f
}

func createACLInput(resource, definition string) contract.KVHTTPACLMutationRequest {
	return contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "create", Definition: []byte(definition)}
}

func TestACLCreationPlanOriginalNamesDependenciesAndSingleNativeRequest(t *testing.T) {
	for _, test := range []struct {
		resource, definition string
		reads, deps          int
	}{
		{"token", ` {"Description":"original exact JSON","Policies":[{"Name":"originalpolicy"}],"Roles":[{"Name":"ORIGINALROLE"}]} `, 5, 2},
		{"policy", `{"Name":"NewPolicy","Rules":"acl = \"read\""}`, 1, 0},
		{"role", `{"Name":"NewRole","Policies":[{"ID":"` + impactPolicy + `","Name":"ignored by native ID"},{"ID":"` + impactPolicy + `"}]}`, 2, 1},
		{"auth-method", `{"Name":"NewAuth","Type":"jwt","Config":{"Synthetic":"authored"}}`, 1, 0},
		{"binding-rule", `{"AuthMethod":"OriginalAuth","BindType":"service","BindName":"service"}`, 2, 1},
	} {
		t.Run(test.resource, func(t *testing.T) {
			f := newACLCreationFixture(t)
			input := createACLInput(test.resource, test.definition)
			original := input.Clone()
			p, preview, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
			if err != nil || p == nil || !preview.Complete || preview.Creation == nil || !reflect.DeepEqual(*preview.Creation, original) || len(preview.Reads) != test.reads || len(preview.Dependencies) != test.deps || len(preview.References) != 0 || preview.UpsertIdentity != "" {
				t.Fatal("original creation preview unavailable", err)
			}
			if CompareACLImpact(preview, preview.Clone()) != nil {
				t.Fatal("original creation observation cannot validate itself")
			}
			input.Definition[0] = 'x'
			inspect := p.Impact()
			inspect.Creation.Definition[0] = 'x'
			if len(inspect.Dependencies) > 0 {
				inspect.Dependencies[0].Locations[0].Row = 99
			}
			if !reflect.DeepEqual(p.Impact(), preview) || !reflect.DeepEqual(p.Request(), original) {
				t.Fatal("creation plan retained caller aliases")
			}
			out, err := f.l.ExecuteHTTPACLMutation(context.Background(), p)
			if err != nil || !out.Receipt.Completed || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || CompareACLImpact(preview, out.Impact.Recheck) != nil || f.writes.Load() != 1 || int(f.reads.Load()) != 2*test.reads {
				t.Fatal("original planned creation failed", err)
			}
			f.mu.Lock()
			body := bytes.Clone(f.body)
			f.mu.Unlock()
			if !bytes.Equal(body, original.Definition) || !reflect.DeepEqual(out, f.l.LastHTTPACLMutation()) || f.l.LastResult().HTTP != nil || f.l.s.reserved != 0 {
				t.Fatal("original creation body/evidence escaped or reservation leaked")
			}
			if _, err := f.l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("original creation replayed")
			}
		})
	}
}

func TestACLCreationPlanRefusesCaseInsensitiveNamesAndCrossDomainTokenIDs(t *testing.T) {
	inputs := []contract.KVHTTPACLMutationRequest{createACLInput("policy", `{"Name":"ORIGINALPOLICY"}`), createACLInput("role", `{"Name":"originalrole"}`)}
	for _, field := range []string{"AccessorID", "SecretID"} {
		for _, value := range []string{impactDirect, creationSecret, strings.ToUpper(creationSecret)} {
			raw, _ := json.Marshal(map[string]string{field: value})
			inputs = append(inputs, createACLInput("token", string(raw)))
		}
	}
	for _, input := range inputs {
		f := newACLCreationFixture(t)
		p, o, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
		if !errors.Is(err, ErrConflict) || p != nil || o.Complete || o.Creation == nil || len(o.Reads) != 1 || f.writes.Load() != 0 || f.l.s.reserved != 0 {
			t.Fatal("native collision domain bypassed", err)
		}
	}
	f := newACLCreationFixture(t)
	f.missingSecret.Store(true)
	p, o, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), createACLInput("token", `{"AccessorID":"`+aclMutationOtherID+`"}`), aclPlanLimits())
	if err == nil || p != nil || o.Complete || len(o.Reads) != 1 {
		t.Fatal("missing native token secrets proved cross-domain absence")
	}
}

func TestACLCreationAuthMethodUpsertReadsCompleteOriginalAndInboundReferences(t *testing.T) {
	f := newACLCreationFixture(t)
	input := createACLInput("auth-method", `{"Name":"OriginalAuth","Type":"jwt","Config":{"Synthetic":"replacement"}}`)
	p, preview, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil || p == nil || !preview.Complete || preview.UpsertIdentity != "OriginalAuth" || len(preview.Reads) != 4 || len(preview.References) != 2 || preview.Reads[0].Result.Projection.Rows[0].ConfigState != "absent" || preview.Reads[1].Result.Projection.Rows[0].ConfigState != "value" {
		t.Fatal("same-name native upsert used incomplete list stub", err)
	}
	for _, ref := range preview.References {
		for _, loc := range ref.Locations {
			if preview.Reads[loc.Read].Result.Projection.Rows[loc.Row].Identity != ref.Identity {
				t.Fatal("upsert references lost original read positions")
			}
		}
	}
	f.changed.Store(true)
	out, err := f.l.ExecuteHTTPACLMutation(context.Background(), p)
	if !errors.Is(err, ErrConflict) || out.Receipt.Attempted || out.Impact == nil || out.Impact.Revalidated || !out.Impact.Recheck.Complete || f.writes.Load() != 0 {
		t.Fatal("changed full AuthMethod configuration bypassed upsert revalidation")
	}
	if preview.Reads[0].BodySHA256 != out.Impact.Recheck.Reads[0].BodySHA256 || preview.Reads[1].BodySHA256 == out.Impact.Recheck.Reads[1].BodySHA256 {
		t.Fatal("fixture did not retain unchanged list stub and changed detail")
	}
	p, preview, err = f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	out, err = f.l.ExecuteHTTPACLMutation(context.Background(), p)
	if err != nil || !out.Impact.Revalidated || out.Impact.Preview.UpsertIdentity != "OriginalAuth" || out.Receipt.HTTP.Request.Path != "/v1/acl/auth-method" || !bytes.Equal(out.Receipt.HTTP.Request.Body, input.Definition) {
		t.Fatal("native upsert was converted to another request", err)
	}
}

func TestACLCreationDependenciesChangedWithoutIndexChangePreventWrites(t *testing.T) {
	for _, definition := range []string{`{"Policies":[{"ID":"` + impactPolicy + `"}]}`, `{"Roles":[{"ID":"` + impactRole + `"}]}`} {
		f := newACLCreationFixture(t)
		p, preview, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), createACLInput("token", definition), aclPlanLimits())
		if err != nil {
			t.Fatal(err)
		}
		f.changed.Store(true)
		out, err := f.l.ExecuteHTTPACLMutation(context.Background(), p)
		if !errors.Is(err, ErrConflict) || out.Receipt.Attempted || out.Impact.Revalidated || !out.Impact.Recheck.Complete || f.writes.Load() != 0 {
			t.Fatal("changed direct or role-derived policy rules authorized creation")
		}
		if !reflect.DeepEqual(preview.Dependencies, out.Impact.Recheck.Dependencies) {
			t.Fatal("fixture changed original dependency identities")
		}
	}
}

func TestACLCreationBoundsIncompleteDependenciesAndForgedObservations(t *testing.T) {
	input := createACLInput("token", `{"Roles":[{"Name":"OriginalRole"}]}`)
	for _, mode := range []string{"reads", "rows", "bytes", "filtered", "missing-name", "missing-id", "upsert-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newACLCreationFixture(t)
			request := input.Clone()
			limits := aclPlanLimits()
			switch mode {
			case "reads":
				limits.MaxReads = 1
			case "rows":
				limits.MaxRows = 1
			case "bytes":
				limits.MaxBytes = 1
			case "filtered":
				f.filtered.Store(true)
			case "missing-name":
				request = createACLInput("token", `{"Policies":[{"Name":"missing"}]}`)
			case "missing-id":
				request = createACLInput("token", `{"Policies":[{"ID":"`+aclMutationOtherID+`"}]}`)
			case "upsert-limit":
				request = createACLInput("auth-method", `{"Name":"OriginalAuth","Type":"jwt"}`)
				limits.MaxReads = 1
			}
			p, o, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), request, limits)
			if err == nil || p != nil || o.Complete || f.writes.Load() != 0 || f.l.s.reserved != 0 {
				t.Fatal("incomplete original creation evidence granted authority", err)
			}
			if mode == "reads" || mode == "upsert-limit" {
				if !errors.Is(err, ErrBounds) || len(o.Reads) != 1 || f.reads.Load() != 1 {
					t.Fatal("global creation read bound discarded original receipt or continued", err)
				}
			}
		})
	}
	f := newACLCreationFixture(t)
	p, o, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, edit := range []func(*contract.KVHTTPACLImpact){
		func(o *contract.KVHTTPACLImpact) { o.Dependencies = nil; o.Bytes = aclImpactCost(*o) },
		func(o *contract.KVHTTPACLImpact) {
			o.Reads = o.Reads[:1]
			o.Dependencies = nil
			o.Rows = 1
			o.Bytes = aclImpactCost(*o)
		},
		func(o *contract.KVHTTPACLImpact) { o.Reads[0].BodySHA256 = "forged" },
		func(o *contract.KVHTTPACLImpact) { o.Reads[1].Result.Scope.Generation++ },
	} {
		copy := o.Clone()
		edit(&copy)
		if CompareACLImpact(copy, copy.Clone()) == nil {
			t.Fatal("forged creation dependency evidence validated")
		}
	}
}

func TestACLCreationUnknownKeepsOriginalDefinitionAndDependencyEvidenceOnRecovery(t *testing.T) {
	f := newACLCreationFixture(t)
	input := createACLInput("role", `{"Name":"NewRole","Policies":[{"ID":"`+impactPolicy+`"}]}`)
	p, preview, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.unknown.Store(true)
	out, err := f.l.ExecuteHTTPACLMutation(context.Background(), p)
	if err == nil || !out.Receipt.WriteOutcomeUnknown || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) {
		t.Fatal("unknown native create lost original evidence")
	}
	before := f.reads.Load()
	recovered, err := f.l.s.RecoverKV(f.l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if recovered.ACLMutationResult == nil || !reflect.DeepEqual(*recovered.ACLMutationResult, out) || f.reads.Load() != before || f.writes.Load() != 1 || recovered.Last.HTTP != nil || recovered.Unknown != nil {
		t.Fatal("pure creation recovery changed original evidence or replayed I/O")
	}
	recovered.ACLMutationResult.Impact.Preview.Creation.Definition[0] = 'x'
	recovered.ACLMutationResult.Impact.Recheck.Dependencies[0].Locations[0].Row = 99
	if !reflect.DeepEqual(recovered.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), out) {
		t.Fatal("creation recovery aliases private evidence")
	}
	if _, _, err := recovered.Lease.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits()); !errors.Is(err, contract.ErrKVWriteUnknown) || f.reads.Load() != before {
		t.Fatal("unknown creation recovery authorized more writes")
	}
}
