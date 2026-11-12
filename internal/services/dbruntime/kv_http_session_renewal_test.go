package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type httpSessionRenewalHandle struct {
	*handle
	scope   contract.KVScope
	reserve func(uint64) (contract.KVHTTPSessionRenewalLease, error)
	recover func(contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error)
}

func (h *httpSessionRenewalHandle) KVScope() contract.KVScope { return h.scope.Clone() }
func (h *httpSessionRenewalHandle) ReserveKVHTTPSessionRenewal(id uint64) (contract.KVHTTPSessionRenewalLease, error) {
	return h.reserve(id)
}
func (h *httpSessionRenewalHandle) RecoverKVHTTPSessionRenewal(l contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
	return h.recover(l)
}
func httpSessionRenewalHandleFor(id int64) *httpSessionRenewalHandle {
	h := kvHandleFor(id)
	h.scope.Header = contract.KVHeader{}
	h.scope.HTTP = contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "https://original", Configuration: "original", AgentID: "original-agent", AgentAvailable: true}
	return &httpSessionRenewalHandle{handle: h.handle, scope: h.scope}
}

type httpSessionRenewalFakePreparation struct {
	request contract.KVHTTPSessionRenewalRequest
}

func (p *httpSessionRenewalFakePreparation) Snapshot() contract.KVHTTPSessionRenewalRequest {
	return p.request
}

type httpSessionRenewalFakeLease struct {
	scope   contract.KVScope
	closed  atomic.Int32
	prepare func(contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error)
	start   func(contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error)
	last    *contract.KVHTTPSessionRenewalSnapshot
}

func (l *httpSessionRenewalFakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *httpSessionRenewalFakeLease) CheckKVLease() error {
	if l.closed.Load() > 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (l *httpSessionRenewalFakeLease) Limits() contract.KVHTTPSessionRenewalLimits {
	return contract.KVHTTPSessionRenewalLimits{MaxRenewals: 256}
}
func (l *httpSessionRenewalFakeLease) Prepare(r contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error) {
	if l.prepare != nil {
		return l.prepare(r)
	}
	return &httpSessionRenewalFakePreparation{r}, nil
}
func (l *httpSessionRenewalFakeLease) Start(_ context.Context, p contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
	return l.start(p)
}
func (l *httpSessionRenewalFakeLease) LastRenewal() *contract.KVHTTPSessionRenewalSnapshot {
	return l.last
}
func (l *httpSessionRenewalFakeLease) ClearReceipts() error { l.last = nil; return nil }
func (l *httpSessionRenewalFakeLease) Close() error         { l.closed.Add(1); return nil }

type httpSessionRenewalFakeRun struct {
	request  contract.KVHTTPSessionRenewalRequest
	snapshot contract.KVHTTPSessionRenewalSnapshot
	closed   atomic.Int32
}

func (w *httpSessionRenewalFakeRun) Request() contract.KVHTTPSessionRenewalRequest { return w.request }
func (w *httpSessionRenewalFakeRun) Snapshot() contract.KVHTTPSessionRenewalSnapshot {
	return w.snapshot
}
func (w *httpSessionRenewalFakeRun) Poll() contract.KVHTTPSessionRenewalBatch {
	return w.snapshot.KVHTTPSessionRenewalBatch
}
func (w *httpSessionRenewalFakeRun) Stop(context.Context) error { return nil }
func (w *httpSessionRenewalFakeRun) Close() error               { w.closed.Add(1); return nil }

func TestHTTPSessionRenewalRuntimeLateOriginalRunAndImmutableReceiptsSurviveReplacement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var starts, recoveries atomic.Int32
	var originalRun *httpSessionRenewalFakeRun
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := httpSessionRenewalHandleFor(p.ID)
		h.reserve = func(uint64) (contract.KVHTTPSessionRenewalLease, error) {
			l := &httpSessionRenewalFakeLease{scope: h.scope.Clone()}
			l.start = func(p contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
				starts.Add(1)
				close(entered)
				<-release
				index := uint64(math.MaxUint64)
				originalRun = &httpSessionRenewalFakeRun{request: p.Snapshot(), snapshot: contract.KVHTTPSessionRenewalSnapshot{Request: p.Snapshot(), KVHTTPSessionRenewalBatch: contract.KVHTTPSessionRenewalBatch{Ended: true, Observations: []contract.KVHTTPSessionRenewalObservation{{HTTP: contract.KVHTTPResult{Body: []byte{0, 255}, Metadata: contract.KVHTTPMetadata{Index: &index}}}}}}}
				return originalRun, nil
			}
			return l, nil
		}
		h.recover = func(old contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
			recoveries.Add(1)
			v := originalRun.Snapshot()
			return contract.KVHTTPSessionRenewalRecovery{Lease: &httpSessionRenewalFakeLease{scope: h.scope.Clone()}, Renewal: &v}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ := m.KVHTTPSessionRenewal(1)
	l, _ := p.ReserveKVHTTPSessionRenewal(1)
	defer l.Close()
	request := contract.KVHTTPSessionRenewalRequest{SessionID: "12345678-1234-4234-8234-123456789abc", MaxRenewals: 1}
	prepared, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		w   contract.KVHTTPSessionRenewalRun
		err error
	}
	out := make(chan result, 1)
	go func() { w, e := l.Start(context.Background(), prepared); out <- result{w, e} }()
	kvAwait(t, entered)
	if err = m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	got := <-out
	if got.w == nil || !errors.Is(got.err, contract.ErrRevoked) || starts.Load() != 1 || got.w.Request() != request || got.w.Snapshot().Observations[0].HTTP.Metadata.Index == nil {
		t.Fatal("late original run/evidence substituted", got.err)
	}
	snapshot := got.w.Snapshot()
	snapshot.Observations[0].HTTP.Body[0] = 9
	*snapshot.Observations[0].HTTP.Metadata.Index = 1
	if !bytes.Equal(got.w.Poll().Observations[0].HTTP.Body, []byte{0, 255}) || *got.w.Snapshot().Observations[0].HTTP.Metadata.Index != math.MaxUint64 {
		t.Fatal("inspection mutated native archive")
	}
	fresh, _ := m.KVHTTPSessionRenewal(1)
	recovery, err := fresh.RecoverKVHTTPSessionRenewal(l)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation <= l.Scope().Generation || recoveries.Load() != 1 || starts.Load() != 1 {
		t.Fatal("recovery resumed/substituted Watch", err)
	}
	defer recovery.Lease.Close()
}

func TestHTTPSessionRenewalRuntimeRejectsChangedPreparationAndForeignRun(t *testing.T) {
	for _, mode := range []string{"prepare", "run", "foreign-plan"} {
		t.Run(mode, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := httpSessionRenewalHandleFor(1)
			var started atomic.Int32
			var rawRun *httpSessionRenewalFakeRun
			h.reserve = func(uint64) (contract.KVHTTPSessionRenewalLease, error) {
				l := &httpSessionRenewalFakeLease{scope: h.scope.Clone()}
				l.prepare = func(r contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error) {
					if mode == "prepare" {
						r.MaxRenewals++
					}
					return &httpSessionRenewalFakePreparation{r}, nil
				}
				l.start = func(p contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
					started.Add(1)
					r := p.Snapshot()
					if mode == "run" {
						r.SessionID = "foreign"
					}
					rawRun = &httpSessionRenewalFakeRun{request: r}
					return rawRun, nil
				}
				return l, nil
			}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVHTTPSessionRenewal(1)
			l, _ := provider.ReserveKVHTTPSessionRenewal(1)
			defer l.Close()
			r := contract.KVHTTPSessionRenewalRequest{SessionID: "12345678-1234-4234-8234-123456789abc"}
			p, err := l.Prepare(r)
			if mode == "prepare" {
				if !errors.Is(err, contract.ErrRevoked) || started.Load() != 0 {
					t.Fatal("altered range authorized")
				}
				return
			}
			if mode == "foreign-plan" {
				other, _ := provider.ReserveKVHTTPSessionRenewal(2)
				defer other.Close()
				if _, err = other.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || started.Load() != 0 {
					t.Fatal("foreign preparation executed")
				}
				return
			}
			if _, err = l.Start(context.Background(), p); !errors.Is(err, contract.ErrRevoked) || rawRun.closed.Load() != 1 {
				t.Fatal("foreign run was admitted/left open", err)
			}
		})
	}
}

func TestHTTPSessionRenewalRuntimeForeignScopeRejectedBeforeRecovery(t *testing.T) {
	for _, change := range []func(*contract.KVScope){
		func(s *contract.KVScope) { s.HTTP.Endpoint = "https://foreign" }, func(s *contract.KVScope) { s.HTTP.AgentID = "foreign" }, func(s *contract.KVScope) { s.HTTP.Configuration = "foreign" }, func(s *contract.KVScope) { s.Prefix = []byte("foreign") }, func(s *contract.KVScope) { s.Header.ClusterID = 1 },
	} {
		m := dbruntime.New()
		var opens, recoveries atomic.Int32
		var oldRaw *httpSessionRenewalFakeLease
		m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
			h := httpSessionRenewalHandleFor(1)
			if opens.Add(1) > 1 {
				change(&h.scope)
			}
			h.reserve = func(uint64) (contract.KVHTTPSessionRenewalLease, error) {
				l := &httpSessionRenewalFakeLease{scope: h.scope.Clone()}
				oldRaw = l
				return l, nil
			}
			h.recover = func(contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
				recoveries.Add(1)
				return contract.KVHTTPSessionRenewalRecovery{}, nil
			}
			return h, nil
		}))
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		p, _ := m.KVHTTPSessionRenewal(1)
		old, _ := p.ReserveKVHTTPSessionRenewal(1)
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		fresh, _ := m.KVHTTPSessionRenewal(1)
		if _, err := fresh.RecoverKVHTTPSessionRenewal(old); !errors.Is(err, contract.ErrRevoked) || recoveries.Load() != 0 || oldRaw.closed.Load() != 0 {
			t.Fatal("foreign identity altered original evidence", err)
		}
		old.Close()
		m.Close()
	}
}
