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

func TestKVHTTPACLMutationPlanOriginalNestedGenerationsRecoveryAndNoReplay(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			var reads, writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Consul-Index", "84")
				switch r.URL.Path {
				case "/v1/agent/self":
					fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
				case "/v1/status/leader":
					fmt.Fprint(w, `"127.0.0.1:8300"`)
				case "/v1/kv/":
					fmt.Fprint(w, `[]`)
				case "/v1/acl/policy/" + runtimeDestroyUUID:
					if r.Method == "DELETE" {
						writes.Add(1)
						if unknown {
							w.WriteHeader(500)
							fmt.Fprint(w, "original processed reply unavailable")
						} else {
							fmt.Fprint(w, "true")
						}
						return
					}
					reads.Add(1)
					fmt.Fprint(w, `{"ID":"`+runtimeDestroyUUID+`","Name":"original","ModifyIndex":18446744073709551615}`)
				case "/v1/acl/tokens", "/v1/acl/roles":
					reads.Add(1)
					fmt.Fprint(w, "[]")
				default:
					t.Error("unexpected original planned request")
					w.WriteHeader(400)
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
			l, err := provider.ReserveKV(99)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			planning, ok := l.(contract.KVHTTPACLMutationPlanning)
			if !ok {
				t.Fatal("original planning facet missing")
			}
			mutating, ok := l.(contract.KVHTTPACLMutating)
			if !ok {
				t.Fatal("planning hid original private mutation")
			}
			if _, ok := l.(contract.KVHTTPSessionDestroyPlanning); !ok {
				t.Fatal("planning hid Session destroy")
			}
			if _, ok := l.(contract.KVHTTPACLImpactBrowsing); !ok {
				t.Fatal("planning hid impact reads")
			}
			request := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: runtimeDestroyUUID}
			limits := contract.KVHTTPACLImpactLimits{MaxRows: 10000, MaxReads: 128, MaxBytes: 32 << 20}
			p, preview, err := planning.PrepareHTTPACLMutationPlan(context.Background(), request, limits)
			if err != nil || p == nil || !preview.Complete || preview.Scope.Generation != l.Scope().Generation || preview.Scope.Generation <= 1 || !reflect.DeepEqual(p.Impact(), preview) {
				t.Fatal("planned preview publication failed", err)
			}
			for _, read := range preview.Reads {
				if !reflect.DeepEqual(read.Result.Scope, preview.Scope) {
					t.Fatal("nested preview generation not translated")
				}
			}
			foreign, err := provider.ReserveKV(100)
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			if _, err := foreign.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("foreign wrapper accepted original plan")
			}
			out, err := mutating.ExecuteHTTPACLMutation(context.Background(), p)
			if (err != nil) != unknown || out.Receipt.WriteOutcomeUnknown != unknown || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || !reflect.DeepEqual(out, mutating.LastHTTPACLMutation()) || writes.Load() != 1 || reads.Load() != 6 {
				t.Fatal("original planned receipt publication failed", err)
			}
			for _, read := range out.Impact.Recheck.Reads {
				if !reflect.DeepEqual(read.Result.Scope, preview.Scope) {
					t.Fatal("recheck changed original publication")
				}
			}
			if p.Request().Resource != "" || p.Impact().Complete {
				t.Fatal("consumed wrapper retained private plan caches")
			}
			if _, err := mutating.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("published original plan replayed")
			}
			original := out.Clone()
			for range 2 {
				var parked contract.KVHTTPACLMutationPlan
				if !unknown {
					parked, _, err = l.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), request, limits)
					if err != nil {
						t.Fatal(err)
					}
				}
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
				if parked != nil && (parked.Impact().Complete || parked.Request().Resource != "") {
					t.Fatal("pure recovery kept original wrapper plan caches")
				}
				if r.ACLMutationResult == nil || !reflect.DeepEqual(*r.ACLMutationResult, original) || !reflect.DeepEqual(r.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), original) || reads.Load() != before || writes.Load() != 1 {
					t.Fatal("repeated recovery relabeled or replayed original planned evidence")
				}
				if r.Last.HTTP != nil || r.Unknown != nil || r.Lease.Scope().Generation <= original.Scope.Generation {
					t.Fatal("recovery mixed private/generic evidence or generation")
				}
				r.ACLMutationResult.Impact.Preview.Reads[0].Result.Receipt.HTTP.Body[0] = 'x'
				if !reflect.DeepEqual(r.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), original) {
					t.Fatal("recovery aliases original private impact")
				}
				l.Close()
				l = r.Lease
				if unknown {
					if _, _, err := l.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(context.Background(), request, limits); !errors.Is(err, contract.ErrKVWriteUnknown) || reads.Load() != before {
						t.Fatal("unknown recovery authorized another impact write")
					}
				}
			}
		})
	}
}
