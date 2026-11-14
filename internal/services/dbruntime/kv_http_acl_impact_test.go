package dbruntime_test

import (
	"context"
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

func TestKVHTTPACLImpactPublishesEveryOriginalReadAndPreservesOtherFacets(t *testing.T) {
	for _, mode := range []string{"", "foreign-observation", "foreign-read", "foreign-request", "absent-facet"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
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
					reads.Add(1)
					fmt.Fprint(w, `{"ID":"`+runtimeDestroyUUID+`","ModifyIndex":18446744073709551615}`)
				case "/v1/acl/tokens", "/v1/acl/roles":
					reads.Add(1)
					fmt.Fprint(w, `[]`)
				default:
					t.Error("unexpected request during impact observation")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			m := dbruntime.New()
			defer m.Close()
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
				s, err := consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL}, "")
				if err != nil {
					return nil, err
				}
				if mode == "" {
					return s, nil
				}
				return &impactRuntimeSession{Session: s, provider: s, mode: mode}, nil
			}))
			for range 2 {
				if err := m.Connect(context.Background(), profile(1), ""); err != nil {
					t.Fatal(err)
				}
			}
			provider, err := m.KVRequests(1)
			if err != nil || provider == nil {
				t.Fatal("original impact provider missing", err)
			}
			l, err := provider.ReserveKV(91)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			impact, ok := l.(contract.KVHTTPACLImpactBrowsing)
			if mode == "absent-facet" {
				if ok {
					t.Fatal("unsupported observer advertised")
				}
				return
			}
			if !ok {
				t.Fatal("original ACL impact facet missing")
			}
			if _, ok := l.(contract.KVHTTPACLMutating); !ok {
				t.Fatal("impact hides mutation facet")
			}
			if _, ok := l.(contract.KVHTTPACLReading); !ok {
				t.Fatal("impact hides original read facet")
			}
			if mode == "" {
				if _, ok := l.(contract.KVHTTPSessionKeysBrowsing); !ok {
					t.Fatal("impact hides held-key facet")
				}
				if _, ok := l.(contract.KVHTTPSessionDestroyPlanning); !ok {
					t.Fatal("impact hides destroy planning")
				}
			}
			input := contract.KVHTTPACLImpactRequest{Resource: "policy", Identity: runtimeDestroyUUID, MaxRows: 10000, MaxReads: 128, MaxBytes: 32 << 20}
			o, err := impact.ObserveHTTPACLImpact(context.Background(), input)
			if reads.Load() != 3 || len(o.Reads) != 3 || l.LastResult().HTTP != nil {
				t.Fatal("impact lost original reads or exposed generic secret receipts")
			}
			if mode != "" {
				if err == nil || o.Complete {
					t.Fatal("foreign original impact accepted")
				}
				return
			}
			if err != nil || !o.Complete || o.Scope.Generation != l.Scope().Generation || o.Scope.Generation <= 1 {
				t.Fatal("impact publication failed", err)
			}
			for _, r := range o.Reads {
				if !reflect.DeepEqual(r.Result.Scope, o.Scope) {
					t.Fatal("nested original receipt generation not translated")
				}
			}
			if err := consul.CompareACLImpact(o, o.Clone()); err != nil {
				t.Fatal("translated receipt cannot revalidate", err)
			}
			before := reads.Load()
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ = m.KVRequests(1)
			recovery, err := provider.RecoverKV(l)
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.Lease.Close()
			if reads.Load() != before {
				t.Fatal("pure recovery replayed impact reads")
			}
			next := recovery.Lease.(contract.KVHTTPACLImpactBrowsing)
			fresh, err := next.ObserveHTTPACLImpact(context.Background(), input)
			if err != nil || fresh.Scope.Generation <= o.Scope.Generation || !o.Complete {
				t.Fatal("explicit recovered observation relabeled old evidence", err)
			}
			for _, r := range o.Reads {
				if r.Result.Scope.Generation != o.Scope.Generation {
					t.Fatal("recovery mutated old nested evidence")
				}
			}
		})
	}
}

type impactRuntimeSession struct {
	contract.Session
	provider contract.KVRequestsProvider
	mode     string
}

func (s *impactRuntimeSession) KVScope() contract.KVScope { return s.provider.KVScope() }

var _ contract.KVRequestsProvider = (*impactRuntimeSession)(nil)

func (s *impactRuntimeSession) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	l, err := s.provider.ReserveKV(id)
	if err != nil {
		return nil, err
	}
	b := &impactRuntimeBase{KVRequestLease: l, KVHTTPACLReading: l.(contract.KVHTTPACLReading), KVHTTPACLMutating: l.(contract.KVHTTPACLMutating)}
	if s.mode == "absent-facet" {
		return b, nil
	}
	return &impactRuntimeLease{impactRuntimeBase: b, raw: l.(contract.KVHTTPACLImpactBrowsing), mode: s.mode}, nil
}
func (s *impactRuntimeSession) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	return s.provider.RecoverKV(l)
}

type impactRuntimeBase struct {
	contract.KVRequestLease
	contract.KVHTTPACLReading
	contract.KVHTTPACLMutating
}
type impactRuntimeLease struct {
	*impactRuntimeBase
	raw  contract.KVHTTPACLImpactBrowsing
	mode string
}

func (l *impactRuntimeLease) ObserveHTTPACLImpact(ctx context.Context, input contract.KVHTTPACLImpactRequest) (contract.KVHTTPACLImpact, error) {
	o, err := l.raw.ObserveHTTPACLImpact(ctx, input)
	switch l.mode {
	case "foreign-observation":
		o.Scope.Generation++
	case "foreign-read":
		o.Reads[1].Result.Scope.Namespace.ProfileID++
	case "foreign-request":
		o.Request.Identity = "foreign"
	}
	return o, err
}
