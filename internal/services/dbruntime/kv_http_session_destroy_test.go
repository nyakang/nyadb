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

const runtimeDestroyUUID = "11111111-2222-4333-8444-555555555555"

func runtimeDestroyRequest() contract.KVHTTPSessionDestroyImpactRequest {
	return contract.KVHTTPSessionDestroyImpactRequest{ID: runtimeDestroyUUID, MaxRows: 10000, MaxBytes: 32 << 20}
}

func runtimeDestroyFixture(t *testing.T, lost bool, wrappers ...func(contract.KVRequestLease) contract.KVRequestLease) (*dbruntime.Manager, *atomic.Int32) {
	t.Helper()
	puts := new(atomic.Int32)
	exists := new(atomic.Bool)
	exists.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "84")
		switch r.URL.Path {
		case "/v1/agent/self":
			fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
		case "/v1/status/leader":
			fmt.Fprint(w, `"127.0.0.1:8300"`)
		case "/v1/session/info/" + runtimeDestroyUUID:
			if exists.Load() {
				fmt.Fprint(w, `[{"ID":"`+runtimeDestroyUUID+`","Behavior":"release"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/v1/kv/":
			if !r.URL.Query().Has("recurse") {
				fmt.Fprint(w, `[]`)
				return
			}
			session := `null`
			if exists.Load() {
				session = `"` + runtimeDestroyUUID + `"`
			}
			fmt.Fprint(w, `[{"Key":"outside/original","Value":"AP8=","Flags":18446744073709551615,"ModifyIndex":84,"Session":`+session+`}]`)
		case "/v1/session/destroy/" + runtimeDestroyUUID:
			puts.Add(1)
			exists.Store(false)
			if lost {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				connection.Close()
				return
			}
			fmt.Fprint(w, "true")
		default:
			t.Errorf("unexpected original path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		raw, err := consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL}, "")
		if err != nil || len(wrappers) == 0 {
			return raw, err
		}
		return &destroyWrappedSession{Session: raw, provider: raw, wrap: wrappers[0]}, nil
	}))
	for range 2 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
	}
	return m, puts
}

func TestKVHTTPSessionDestroyPublishedPlansKeepOriginalReceiptGenerationThroughRecovery(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			m, puts := runtimeDestroyFixture(t, lost)
			provider, _ := m.KVRequests(1)
			lease, err := provider.ReserveKV(81)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			generation := lease.Scope().Generation
			if generation <= 1 {
				t.Fatal("fixture did not separate raw and published generations")
			}
			p, err := lease.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(context.Background(), runtimeDestroyRequest())
			if err != nil || p.Impact().Scope.Generation != generation || p.Impact().Keys.Scope.Generation != generation || !p.Impact().Complete || puts.Load() != 0 {
				t.Fatal("original preview was retargeted or wrote early", err)
			}
			preview := p.Impact()
			mutated := p.Impact()
			mutated.Keys.Root.HTTP.Body[0] = 'x'
			if !reflect.DeepEqual(p.Impact(), preview) {
				t.Fatal("published preview aliases immutable original bytes")
			}
			foreign, _ := provider.ReserveKV(82)
			defer foreign.Close()
			if _, err := foreign.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || puts.Load() != 0 {
				t.Fatal("foreign publication accepted plan", err)
			}
			result, err := lease.Execute(context.Background(), p)
			if (err != nil) != lost || result.WriteOutcomeUnknown != lost || puts.Load() != 1 || result.HTTPSessionDestroy == nil || result.HTTPSessionDestroy.LocalRecordID == "" || result.HTTPSessionDestroy.LocalRecordID != p.LocalRecordID() || result.HTTPSessionDestroy.Preview.Scope.Generation != generation || result.HTTPSessionDestroy.Recheck.Scope.Generation != generation || !reflect.DeepEqual(result, lease.LastResult()) {
				t.Fatal("original result changed publication or acknowledgement", err)
			}
			if _, err := lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || puts.Load() != 1 || p.Impact().Complete {
				t.Fatal("used publication plan retained authority or replayed", err)
			}
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ = m.KVRequests(1)
			recovery, err := provider.RecoverKV(lease)
			if err != nil || recovery.Lease.Scope().Generation <= generation || !reflect.DeepEqual(recovery.Last, result) || !reflect.DeepEqual(recovery.Lease.LastResult(), result) || puts.Load() != 1 {
				t.Fatal("recovery changed original receipt to the fresh publication", err)
			}
			defer recovery.Lease.Close()
			if lost {
				if recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, result) {
					t.Fatal("original unknown write evidence lost")
				}
				observed, err := recovery.Lease.(contract.KVHTTPSessionKeysBrowsing).ObserveHTTPSessionKeys(context.Background(), contract.KVHTTPSessionKeysRequest{ID: runtimeDestroyUUID, MaxRows: 10000, MaxBytes: 32 << 20})
				if err != nil || !observed.Complete || len(observed.Matches) != 0 {
					t.Fatal("explicit fresh root read failed", err)
				}
				second, err := provider.RecoverKV(recovery.Lease)
				if err != nil || second.Unknown == nil || !reflect.DeepEqual(*second.Unknown, result) || second.Unknown.HTTPSessionDestroy.Preview.Scope.Generation != generation || puts.Load() != 1 {
					t.Fatal("later fresh read/recovery retargeted original unknown evidence", err)
				}
				defer second.Lease.Close()
				if _, err := second.Lease.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(context.Background(), runtimeDestroyRequest()); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("later read authorized original unknown replay", err)
				}
			}
		})
	}
}

func TestKVHTTPSessionDestroyLeaseCloseAndRecoveryClearOriginalPlanWithoutWrites(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			m, puts := runtimeDestroyFixture(t, false)
			provider, _ := m.KVRequests(1)
			lease, _ := provider.ReserveKV(91)
			p, err := lease.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(context.Background(), runtimeDestroyRequest())
			if err != nil {
				t.Fatal(err)
			}
			if recover {
				r, err := provider.RecoverKV(lease)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Lease.Close()
				if _, err := r.Lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("recovery transferred plan authority", err)
				}
			} else if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if p.Impact().Complete || puts.Load() != 0 {
				t.Fatal("close/recovery retained plan preview or wrote")
			}
			if err := p.Close(); err != nil {
				t.Fatal("idempotent published plan close failed", err)
			}
		})
	}
}

func TestKVHTTPSessionDestroyUnsupportedLeasesDoNotAdvertisePlanning(t *testing.T) {
	for _, options := range [][2]bool{{false, true}, {true, false}, {true, true}} {
		m, _ := sessionKeysRuntime(t, options[0], options[1])
		provider, _ := m.KVRequests(1)
		lease, _ := provider.ReserveKV(101)
		if _, ok := lease.(contract.KVHTTPSessionDestroyPlanning); ok {
			t.Fatal("unsupported raw lease or product advertised destroy planning")
		}
		lease.Close()
	}
}

type destroyWrappedSession struct {
	contract.Session
	provider contract.KVRequestsProvider
	wrap     func(contract.KVRequestLease) contract.KVRequestLease
}

func (s *destroyWrappedSession) KVScope() contract.KVScope { return s.provider.KVScope() }
func (s *destroyWrappedSession) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	l, err := s.provider.ReserveKV(id)
	if err != nil {
		return nil, err
	}
	return s.wrap(l), nil
}
func (s *destroyWrappedSession) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	if wrapped, ok := l.(*destroyWrappedLease); ok {
		l = wrapped.KVRequestLease
	}
	r, err := s.provider.RecoverKV(l)
	if r.Lease != nil {
		r.Lease = s.wrap(r.Lease)
	}
	return r, err
}

type destroyWrappedLease struct {
	contract.KVRequestLease
	mode string
}

func (l *destroyWrappedLease) ObserveHTTPSessionKeys(ctx context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
	return l.KVRequestLease.(contract.KVHTTPSessionKeysBrowsing).ObserveHTTPSessionKeys(ctx, r)
}
func corruptDestroyImpact(o contract.KVHTTPSessionDestroyImpact, mode string) contract.KVHTTPSessionDestroyImpact {
	o = o.Clone()
	switch mode {
	case "top-scope":
		o.Scope.Generation++
	case "key-scope":
		o.Keys.Scope.Namespace.ProfileID++
	case "key-request":
		o.Keys.Request.ID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	}
	return o
}
func (l *destroyWrappedLease) ObserveHTTPSessionDestroyImpact(ctx context.Context, r contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyImpact, error) {
	o, err := l.KVRequestLease.(contract.KVHTTPSessionDestroyImpactBrowsing).ObserveHTTPSessionDestroyImpact(ctx, r)
	return corruptDestroyImpact(o, l.mode), err
}
func (l *destroyWrappedLease) PrepareHTTPSessionDestroy(ctx context.Context, r contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
	p, err := l.KVRequestLease.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(ctx, r)
	if p == nil {
		return nil, err
	}
	return &destroyWrappedPlan{KVHTTPSessionDestroyPreparation: p, mode: l.mode}, err
}

type destroyWrappedPlan struct {
	contract.KVHTTPSessionDestroyPreparation
	mode string
}

func (p *destroyWrappedPlan) Impact() contract.KVHTTPSessionDestroyImpact {
	return corruptDestroyImpact(p.KVHTTPSessionDestroyPreparation.Impact(), p.mode)
}
func (p *destroyWrappedPlan) Snapshot() contract.KVRequest {
	if p.mode == "snapshot" {
		r, _ := consul.DocumentRequest(consul.Request{Method: "PUT", Path: "/v1/session/destroy/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"})
		return r
	}
	return p.KVHTTPSessionDestroyPreparation.Snapshot()
}
func TestKVHTTPSessionDestroyRejectsForeignPreviewKeysAndChangedNativeTargetBeforePUT(t *testing.T) {
	for _, mode := range []string{"top-scope", "key-scope", "key-request", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			m, puts := runtimeDestroyFixture(t, false, func(l contract.KVRequestLease) contract.KVRequestLease {
				return &destroyWrappedLease{KVRequestLease: l, mode: mode}
			})
			provider, _ := m.KVRequests(1)
			lease, err := provider.ReserveKV(111)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			facet := lease.(contract.KVHTTPSessionDestroyPlanning)
			plan, err := facet.PrepareHTTPSessionDestroy(context.Background(), runtimeDestroyRequest())
			if !errors.Is(err, contract.ErrRevoked) || plan != nil || puts.Load() != 0 {
				t.Fatal("foreign preview or native target produced authority", err)
			}
			if mode != "snapshot" {
				observed, err := lease.(contract.KVHTTPSessionDestroyImpactBrowsing).ObserveHTTPSessionDestroyImpact(context.Background(), runtimeDestroyRequest())
				if !errors.Is(err, contract.ErrRevoked) || observed.Complete || observed.Session.HTTP == nil || puts.Load() != 0 {
					t.Fatal("foreign read evidence was retargeted or erased", err)
				}
			}
		})
	}
}
