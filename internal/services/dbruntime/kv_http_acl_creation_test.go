package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestKVHTTPACLCreationOriginalDefinitionsUpsertDependenciesAndRecovery(t *testing.T) {
	for _, resource := range []string{"token", "auth-method"} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprint(resource, "/", unknown), func(t *testing.T) {
				var reads, writes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Consul-Index", "84")
					if r.Method == "PUT" {
						writes.Add(1)
						if unknown {
							w.WriteHeader(500)
							fmt.Fprint(w, "original processed reply unavailable")
							return
						}
						if resource == "token" {
							fmt.Fprint(w, `{"AccessorID":"`+runtimeDestroyUUID+`","SecretID":"generated-synthetic-secret","CreateIndex":7,"ModifyIndex":9}`)
						} else {
							fmt.Fprint(w, `{"Name":"OriginalAuth","CreateIndex":7,"ModifyIndex":9}`)
						}
						return
					}
					switch r.URL.Path {
					case "/v1/agent/self":
						fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
					case "/v1/status/leader":
						fmt.Fprint(w, `"127.0.0.1:8300"`)
					case "/v1/kv/":
						fmt.Fprint(w, `[]`)
					case "/v1/acl/tokens", "/v1/acl/binding-rules":
						reads.Add(1)
						fmt.Fprint(w, "[]")
					case "/v1/acl/policies":
						reads.Add(1)
						fmt.Fprint(w, `[{"ID":"`+runtimeDestroyUUID+`","Name":"OriginalPolicy"}]`)
					case "/v1/acl/policy/" + runtimeDestroyUUID:
						reads.Add(1)
						fmt.Fprint(w, `{"ID":"`+runtimeDestroyUUID+`","Name":"OriginalPolicy","Rules":"original rules","ModifyIndex":8}`)
					case "/v1/acl/auth-methods":
						reads.Add(1)
						fmt.Fprint(w, `[{"Name":"OriginalAuth","Type":"jwt"}]`)
					case "/v1/acl/auth-method/OriginalAuth":
						reads.Add(1)
						fmt.Fprint(w, `{"Name":"OriginalAuth","Type":"jwt","Config":{"Synthetic":"original config"},"ModifyIndex":8}`)
					default:
						t.Error("unexpected original creation request")
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				m := dbruntime.New()
				defer m.Close()
				m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
					return consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL}, "")
				}))
				for range 2 {
					if err := m.Connect(context.Background(), profile(1), ""); err != nil {
						t.Fatal(err)
					}
				}
				provider, err := m.KVRequests(1)
				if err != nil {
					t.Fatal(err)
				}
				l, err := provider.ReserveKV(113)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				definition := ` {"Policies":[{"Name":"originalpolicy"}]} `
				wantReads := 3
				if resource == "auth-method" {
					definition = ` {"Name":"OriginalAuth","Type":"jwt","Config":{"Synthetic":"replacement"}} `
					wantReads = 4
				}
				input := contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "create", Definition: []byte(definition)}
				limits := contract.KVHTTPACLImpactLimits{MaxRows: 10000, MaxReads: 128, MaxBytes: 32 << 20}
				p, preview, err := l.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), input, limits)
				if err != nil || p == nil || !preview.Complete || preview.Creation == nil || !reflect.DeepEqual(*preview.Creation, input) || preview.Scope.Generation != l.Scope().Generation || preview.Scope.Generation <= 1 || len(preview.Reads) != wantReads {
					t.Fatal("original creation preview publication failed", err)
				}
				if resource == "auth-method" && preview.UpsertIdentity != "OriginalAuth" || resource == "token" && len(preview.Dependencies) != 1 {
					t.Fatal("original upsert/dependency projection missing")
				}
				for _, read := range preview.Reads {
					if !reflect.DeepEqual(read.Result.Scope, preview.Scope) {
						t.Fatal("nested creation read generation not translated")
					}
				}
				out, err := l.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(context.Background(), p)
				if (err != nil) != unknown || out.Receipt.WriteOutcomeUnknown != unknown || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || consul.CompareACLImpact(preview, out.Impact.Recheck) != nil || writes.Load() != 1 || int(reads.Load()) != 2*wantReads {
					t.Fatal("original creation receipt publication failed", err)
				}
				original := out.Clone()
				for range 2 {
					if err := m.Connect(context.Background(), profile(1), ""); err != nil {
						t.Fatal(err)
					}
					provider, err = m.KVRequests(1)
					if err != nil {
						t.Fatal(err)
					}
					before := reads.Load()
					r, err := provider.RecoverKV(l)
					if err != nil {
						t.Fatal(err)
					}
					defer r.Lease.Close()
					if r.ACLMutationResult == nil || !reflect.DeepEqual(*r.ACLMutationResult, original) || !reflect.DeepEqual(r.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), original) || reads.Load() != before || writes.Load() != 1 || r.Last.HTTP != nil || r.Unknown != nil {
						t.Fatal("creation recovery relabeled original generations or replayed I/O")
					}
					r.ACLMutationResult.Impact.Preview.Creation.Definition[0] = 'x'
					if !reflect.DeepEqual(r.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), original) {
						t.Fatal("creation recovery aliased original definition")
					}
					l.Close()
					l = r.Lease
					if unknown {
						if _, _, err := l.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), input, limits); !errors.Is(err, contract.ErrKVWriteUnknown) || reads.Load() != before {
							t.Fatal("recovery cleared original creation write barrier")
						}
					}
				}
			})
		}
	}
}
