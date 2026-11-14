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

func aclMutationRuntimeFixture(t *testing.T, mode string) (*dbruntime.Manager, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Consul-Index", "84")
		switch r.URL.Path {
		case "/v1/agent/self":
			fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
		case "/v1/status/leader":
			fmt.Fprint(w, `"127.0.0.1:8300"`)
		case "/v1/kv/":
			fmt.Fprint(w, `[]`)
		case "/v1/acl/token":
			if mode == "unknown" {
				w.WriteHeader(500)
				fmt.Fprint(w, "original processed reply withheld")
				return
			}
			fmt.Fprint(w, `{"AccessorID":"`+runtimeDestroyUUID+`","SecretID":"synthetic-test-value","CreateIndex":7,"ModifyIndex":18446744073709551615}`)
		default:
			t.Error("unexpected native dispatch")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		s, err := consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL}, "")
		if err != nil {
			return nil, err
		}
		if mode == "" || mode == "unknown" {
			return s, nil
		}
		return &aclMutationRuntimeSession{Session: s, provider: s, mode: mode}, nil
	}))
	for range 2 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
	}
	return m, calls
}

func TestKVHTTPACLMutationOriginalPublishedReceiptSurvivesRepeatedRecoveryAndFreshWrite(t *testing.T) {
	m, calls := aclMutationRuntimeFixture(t, "")
	provider, _ := m.KVRequests(1)
	l, err := provider.ReserveKV(71)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	acl, ok := l.(contract.KVHTTPACLMutating)
	if !ok {
		t.Fatal("Consul ACL facet missing")
	}
	if _, ok := l.(contract.KVHTTPACLReading); !ok {
		t.Fatal("mutation binding hid private ACL reading")
	}
	if _, ok := l.(contract.KVHTTPSessionKeysBrowsing); !ok {
		t.Fatal("ACL binding hid existing held-key browsing")
	}
	if _, ok := l.(contract.KVHTTPSessionDestroyPlanning); !ok {
		t.Fatal("ACL binding hid existing session destroy planning")
	}
	before := calls.Load()
	request := contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(`{"Description":"original"}`)}
	p, err := acl.PrepareHTTPACLMutation(request)
	if err != nil || calls.Load() != before {
		t.Fatal("published ACL preparation performed I/O", err)
	}
	if _, ok := p.(contract.KVPreparation); ok {
		t.Fatal("private ACL plan advertises generic execution")
	}
	foreign, err := provider.ReserveKV(72)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err := foreign.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("foreign published lease accepted original ACL plan")
	}
	r, err := acl.ExecuteHTTPACLMutation(context.Background(), p)
	if err != nil || r.LocalRecordID == "" || r.LocalRecordID != p.LocalRecordID() || r.Scope.Generation <= 1 || r.Scope.Generation != l.Scope().Generation || !reflect.DeepEqual(r, acl.LastHTTPACLMutation()) || l.LastResult().HTTP != nil {
		t.Fatal("published ACL result lost original identity or entered generic cache", err)
	}
	before = calls.Load()
	if _, err := acl.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("published ACL preparation replayed")
	}
	original := r.Clone()
	for range 2 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		provider, _ = m.KVRequests(1)
		before = calls.Load()
		recovery, err := provider.RecoverKV(l)
		if err != nil || recovery.Lease == nil {
			t.Fatal("published ACL pure recovery failed", err)
		}
		next := recovery.Lease
		defer next.Close()
		nextACL := next.(contract.KVHTTPACLMutating)
		if calls.Load() != before || recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, original) || !reflect.DeepEqual(nextACL.LastHTTPACLMutation(), original) || next.Scope().Generation <= original.Scope.Generation || recovery.Last.HTTP != nil || recovery.Unknown != nil {
			t.Fatal("recovery relabeled old ACL receipt as current or exposed it to generic flows")
		}
		recovery.ACLMutationResult.Receipt.HTTP.Body[0] = 'x'
		if !reflect.DeepEqual(nextACL.LastHTTPACLMutation(), original) {
			t.Fatal("published recovery aliases private ACL evidence")
		}
		l.Close()
		if !reflect.DeepEqual(nextACL.LastHTTPACLMutation(), original) {
			t.Fatal("late original wrapper close erased recovered ACL evidence")
		}
		if _, err := nextACL.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
			t.Fatal("recovery reused original ACL authority")
		}
		l, acl = next, nextACL
	}
	fresh, err := acl.PrepareHTTPACLMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	r, err = acl.ExecuteHTTPACLMutation(context.Background(), fresh)
	if err != nil || r.LocalRecordID == original.LocalRecordID || r.Scope.Generation != l.Scope().Generation || r.Scope.Generation == original.Scope.Generation || !reflect.DeepEqual(r, acl.LastHTTPACLMutation()) {
		t.Fatal("new explicit mutation inherited an old publication", err)
	}
	before = calls.Load()
	recovery, err := provider.RecoverKV(l)
	if err != nil || recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, r) || calls.Load() != before {
		t.Fatal("subsequent recovery changed fresh ACL receipt", err)
	}
	defer recovery.Lease.Close()
	if !reflect.DeepEqual(recovery.Lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), r) {
		t.Fatal("new original ACL basis lost after recovery")
	}
}

func TestKVHTTPACLMutationUnsupportedProvidersDoNotAdvertisePrivateMutation(t *testing.T) {
	for _, options := range [][2]bool{{false, true}, {true, false}, {true, true}} {
		m, _ := sessionKeysRuntime(t, options[0], options[1])
		provider, _ := m.KVRequests(1)
		l, _ := provider.ReserveKV(1)
		if _, ok := l.(contract.KVHTTPACLMutating); ok {
			t.Fatal("unsupported provider advertised ACL reading")
		}
		l.Close()
	}
}

type aclMutationRuntimeSession struct {
	contract.Session
	provider contract.KVRequestsProvider
	mode     string
}

func (s *aclMutationRuntimeSession) KVScope() contract.KVScope { return s.provider.KVScope() }
func (s *aclMutationRuntimeSession) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	l, err := s.provider.ReserveKV(id)
	if err != nil {
		return nil, err
	}
	return &aclMutationRuntimeLease{KVRequestLease: l, KVHTTPACLReading: l.(contract.KVHTTPACLReading), mode: s.mode}, nil
}
func (s *aclMutationRuntimeSession) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	if wrapped, ok := l.(*aclMutationRuntimeLease); ok {
		l = wrapped.KVRequestLease
	}
	r, err := s.provider.RecoverKV(l)
	if r.Lease != nil {
		r.Lease = &aclMutationRuntimeLease{KVRequestLease: r.Lease, KVHTTPACLReading: r.Lease.(contract.KVHTTPACLReading), mode: s.mode}
	}
	if s.mode == "recovery-scope" && r.ACLMutationResult != nil {
		r.ACLMutationResult.Scope.Namespace.ProfileID++
	}
	return r, err
}

type aclMutationRuntimeLease struct {
	contract.KVRequestLease
	contract.KVHTTPACLReading
	mode string
}

func (l *aclMutationRuntimeLease) PrepareHTTPACLMutation(request contract.KVHTTPACLMutationRequest) (contract.KVHTTPACLMutationPreparation, error) {
	p, err := l.KVRequestLease.(contract.KVHTTPACLMutating).PrepareHTTPACLMutation(request)
	if err != nil {
		return nil, err
	}
	return &aclMutationRuntimePlan{KVHTTPACLMutationPreparation: p, mode: l.mode}, nil
}
func (l *aclMutationRuntimeLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	if wrapped, ok := p.(*aclMutationRuntimePlan); ok {
		p = wrapped.KVHTTPACLMutationPreparation
	}
	r, err := l.KVRequestLease.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(ctx, p)
	switch l.mode {
	case "result-scope":
		r.Scope.Generation += 100
	case "result-request":
		r.Request.Identity = "foreign"
	case "result-record":
		r.LocalRecordID = "foreign"
	}
	return r, err
}
func (l *aclMutationRuntimeLease) LastHTTPACLMutation() contract.KVHTTPACLMutationResult {
	return l.KVRequestLease.(contract.KVHTTPACLMutating).LastHTTPACLMutation()
}

type aclMutationRuntimePlan struct {
	contract.KVHTTPACLMutationPreparation
	mode string
}

func (p *aclMutationRuntimePlan) Request() contract.KVHTTPACLMutationRequest {
	r := p.KVHTTPACLMutationPreparation.Request()
	if p.mode == "plan-request" {
		r.Identity = "foreign"
	}
	return r
}
func (p *aclMutationRuntimePlan) LocalRecordID() string {
	if p.mode == "plan-record" {
		return ""
	}
	return p.KVHTTPACLMutationPreparation.LocalRecordID()
}

func TestKVHTTPACLMutationRejectsForeignPlanResultAndRecoveryWithoutDiscardingReceipt(t *testing.T) {
	for _, mode := range []string{"plan-request", "plan-record", "result-request", "result-record", "result-scope", "recovery-scope"} {
		t.Run(mode, func(t *testing.T) {
			m, calls := aclMutationRuntimeFixture(t, mode)
			provider, _ := m.KVRequests(1)
			l, err := provider.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			acl := l.(contract.KVHTTPACLMutating)
			before := calls.Load()
			p, err := acl.PrepareHTTPACLMutation(contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(`{"Description":"original"}`)})
			if mode == "plan-request" || mode == "plan-record" {
				if !errors.Is(err, contract.ErrRevoked) || p != nil || calls.Load() != before {
					t.Fatal("foreign ACL preparation accepted or dispatched")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := acl.ExecuteHTTPACLMutation(context.Background(), p)
			if mode == "recovery-scope" {
				if err != nil {
					t.Fatal(err)
				}
				before = calls.Load()
				recovery, err := provider.RecoverKV(l)
				if !errors.Is(err, contract.ErrRevoked) || recovery.Lease != nil || recovery.ACLMutationResult == nil || recovery.ACLMutationResult.Projection != nil || !reflect.DeepEqual(recovery.ACLMutationResult.Receipt, r.Receipt) || calls.Load() != before {
					t.Fatal("foreign recovery scope acquired current publication or lost original evidence")
				}
				return
			}
			if !errors.Is(err, contract.ErrRevoked) || r.Projection != nil || r.Receipt.HTTP == nil || !r.Receipt.Completed || len(r.Receipt.HTTP.Body) == 0 || r.Scope.Generation == l.Scope().Generation {
				t.Fatal("foreign ACL result acquired publication identity or original receipt was discarded")
			}
		})
	}
}

func TestKVHTTPACLMutationUnknownRetainsOriginalPublicationAcrossRecoveries(t *testing.T) {
	m, calls := aclMutationRuntimeFixture(t, "unknown")
	provider, _ := m.KVRequests(1)
	l, err := provider.ReserveKV(73)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	request := contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(`{}`)}
	acl := l.(contract.KVHTTPACLMutating)
	p, err := acl.PrepareHTTPACLMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	original, err := acl.ExecuteHTTPACLMutation(context.Background(), p)
	if err == nil || !original.Receipt.WriteOutcomeUnknown || original.Scope.Generation != l.Scope().Generation {
		t.Fatal("unknown mutation promoted or relabeled")
	}
	for range 3 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		provider, _ = m.KVRequests(1)
		before := calls.Load()
		recovery, err := provider.RecoverKV(l)
		if err != nil {
			t.Fatal(err)
		}
		next := recovery.Lease
		defer next.Close()
		acl = next.(contract.KVHTTPACLMutating)
		if calls.Load() != before || recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, original) || !reflect.DeepEqual(acl.LastHTTPACLMutation(), original) || recovery.Unknown != nil || recovery.Last.HTTP != nil || recovery.ACLResult != nil {
			t.Fatal("recovery replayed, relabeled or exposed private unknown write")
		}
		if _, err := acl.PrepareHTTPACLMutation(request); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
			t.Fatal("runtime recovery removed unknown-write barrier")
		}
		l.Close()
		l = next
	}
}
