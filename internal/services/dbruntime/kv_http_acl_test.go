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

func aclRuntimeFixture(t *testing.T, mode string) (*dbruntime.Manager, *atomic.Int32) {
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
		case "/v1/acl/token/self":
			fmt.Fprint(w, `{"AccessorID":"`+runtimeDestroyUUID+`","SecretID":"synthetic-test-value","CreateIndex":18446744073709551615}`)
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
		if mode == "" {
			return s, nil
		}
		return &aclRuntimeSession{Session: s, provider: s, mode: mode}, nil
	}))
	for range 2 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
	}
	return m, calls
}

func TestKVHTTPACLOriginalPublishedReceiptSurvivesRepeatedRecoveryAndFreshRead(t *testing.T) {
	m, calls := aclRuntimeFixture(t, "")
	provider, _ := m.KVRequests(1)
	l, err := provider.ReserveKV(71)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	acl, ok := l.(contract.KVHTTPACLReading)
	if !ok {
		t.Fatal("Consul ACL facet missing")
	}
	if _, ok := l.(contract.KVHTTPSessionKeysBrowsing); !ok {
		t.Fatal("ACL binding hid existing held-key browsing")
	}
	if _, ok := l.(contract.KVHTTPSessionDestroyPlanning); !ok {
		t.Fatal("ACL binding hid existing session destroy planning")
	}
	before := calls.Load()
	request := contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}
	p, err := acl.PrepareHTTPACLRead(request)
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
	if _, err := foreign.(contract.KVHTTPACLReading).ExecuteHTTPACLRead(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("foreign published lease accepted original ACL plan")
	}
	r, err := acl.ExecuteHTTPACLRead(context.Background(), p)
	if err != nil || r.LocalRecordID == "" || r.LocalRecordID != p.LocalRecordID() || r.Scope.Generation <= 1 || r.Scope.Generation != l.Scope().Generation || !reflect.DeepEqual(r, acl.LastHTTPACLRead()) || l.LastResult().HTTP != nil {
		t.Fatal("published ACL result lost original identity or entered generic cache", err)
	}
	before = calls.Load()
	if _, err := acl.ExecuteHTTPACLRead(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
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
		nextACL := next.(contract.KVHTTPACLReading)
		if calls.Load() != before || recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, original) || !reflect.DeepEqual(nextACL.LastHTTPACLRead(), original) || next.Scope().Generation <= original.Scope.Generation || recovery.Last.HTTP != nil || recovery.Unknown != nil {
			t.Fatal("recovery relabeled old ACL receipt as current or exposed it to generic flows")
		}
		recovery.ACLResult.Receipt.HTTP.Body[0] = 'x'
		if !reflect.DeepEqual(nextACL.LastHTTPACLRead(), original) {
			t.Fatal("published recovery aliases private ACL evidence")
		}
		l.Close()
		if !reflect.DeepEqual(nextACL.LastHTTPACLRead(), original) {
			t.Fatal("late original wrapper close erased recovered ACL evidence")
		}
		if _, err := nextACL.ExecuteHTTPACLRead(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
			t.Fatal("recovery reused original ACL authority")
		}
		l, acl = next, nextACL
	}
	fresh, err := acl.PrepareHTTPACLRead(request)
	if err != nil {
		t.Fatal(err)
	}
	r, err = acl.ExecuteHTTPACLRead(context.Background(), fresh)
	if err != nil || r.LocalRecordID == original.LocalRecordID || r.Scope.Generation != l.Scope().Generation || r.Scope.Generation == original.Scope.Generation || !reflect.DeepEqual(r, acl.LastHTTPACLRead()) {
		t.Fatal("new explicit ACL read inherited an old publication", err)
	}
	before = calls.Load()
	recovery, err := provider.RecoverKV(l)
	if err != nil || recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, r) || calls.Load() != before {
		t.Fatal("subsequent recovery changed fresh ACL receipt", err)
	}
	defer recovery.Lease.Close()
	if !reflect.DeepEqual(recovery.Lease.(contract.KVHTTPACLReading).LastHTTPACLRead(), r) {
		t.Fatal("new original ACL basis lost after recovery")
	}
}

func TestKVHTTPACLUnsupportedProvidersDoNotAdvertisePrivateReading(t *testing.T) {
	for _, options := range [][2]bool{{false, true}, {true, false}, {true, true}} {
		m, _ := sessionKeysRuntime(t, options[0], options[1])
		provider, _ := m.KVRequests(1)
		l, _ := provider.ReserveKV(1)
		if _, ok := l.(contract.KVHTTPACLReading); ok {
			t.Fatal("unsupported provider advertised ACL reading")
		}
		l.Close()
	}
}

type aclRuntimeSession struct {
	contract.Session
	provider contract.KVRequestsProvider
	mode     string
}

func (s *aclRuntimeSession) KVScope() contract.KVScope { return s.provider.KVScope() }
func (s *aclRuntimeSession) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	l, err := s.provider.ReserveKV(id)
	if err != nil {
		return nil, err
	}
	return &aclRuntimeLease{KVRequestLease: l, mode: s.mode}, nil
}
func (s *aclRuntimeSession) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	if wrapped, ok := l.(*aclRuntimeLease); ok {
		l = wrapped.KVRequestLease
	}
	r, err := s.provider.RecoverKV(l)
	if r.Lease != nil {
		r.Lease = &aclRuntimeLease{KVRequestLease: r.Lease, mode: s.mode}
	}
	if s.mode == "recovery-scope" && r.ACLResult != nil {
		r.ACLResult.Scope.Namespace.ProfileID++
	}
	return r, err
}

type aclRuntimeLease struct {
	contract.KVRequestLease
	mode string
}

func (l *aclRuntimeLease) PrepareHTTPACLRead(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadPreparation, error) {
	p, err := l.KVRequestLease.(contract.KVHTTPACLReading).PrepareHTTPACLRead(request)
	if err != nil {
		return nil, err
	}
	return &aclRuntimePlan{KVHTTPACLReadPreparation: p, mode: l.mode}, nil
}
func (l *aclRuntimeLease) ExecuteHTTPACLRead(ctx context.Context, p contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
	if wrapped, ok := p.(*aclRuntimePlan); ok {
		p = wrapped.KVHTTPACLReadPreparation
	}
	r, err := l.KVRequestLease.(contract.KVHTTPACLReading).ExecuteHTTPACLRead(ctx, p)
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
func (l *aclRuntimeLease) LastHTTPACLRead() contract.KVHTTPACLReadResult {
	return l.KVRequestLease.(contract.KVHTTPACLReading).LastHTTPACLRead()
}

type aclRuntimePlan struct {
	contract.KVHTTPACLReadPreparation
	mode string
}

func (p *aclRuntimePlan) Request() contract.KVHTTPACLReadRequest {
	r := p.KVHTTPACLReadPreparation.Request()
	if p.mode == "plan-request" {
		r.Identity = "foreign"
	}
	return r
}
func (p *aclRuntimePlan) LocalRecordID() string {
	if p.mode == "plan-record" {
		return ""
	}
	return p.KVHTTPACLReadPreparation.LocalRecordID()
}

func TestKVHTTPACLRejectsForeignPlanResultAndRecoveryWithoutDiscardingReceipt(t *testing.T) {
	for _, mode := range []string{"plan-request", "plan-record", "result-request", "result-record", "result-scope", "recovery-scope"} {
		t.Run(mode, func(t *testing.T) {
			m, calls := aclRuntimeFixture(t, mode)
			provider, _ := m.KVRequests(1)
			l, err := provider.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			acl := l.(contract.KVHTTPACLReading)
			before := calls.Load()
			p, err := acl.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			if mode == "plan-request" || mode == "plan-record" {
				if !errors.Is(err, contract.ErrRevoked) || p != nil || calls.Load() != before {
					t.Fatal("foreign ACL preparation accepted or dispatched")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := acl.ExecuteHTTPACLRead(context.Background(), p)
			if mode == "recovery-scope" {
				if err != nil {
					t.Fatal(err)
				}
				before = calls.Load()
				recovery, err := provider.RecoverKV(l)
				if !errors.Is(err, contract.ErrRevoked) || recovery.Lease != nil || recovery.ACLResult == nil || recovery.ACLResult.Projection != nil || !reflect.DeepEqual(recovery.ACLResult.Receipt, r.Receipt) || calls.Load() != before {
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
