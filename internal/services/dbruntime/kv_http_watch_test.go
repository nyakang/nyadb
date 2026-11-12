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

type httpWatchHandle struct {
	*handle
	scope   contract.KVScope
	reserve func(uint64) (contract.KVHTTPWatchLease, error)
	recover func(contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error)
}

func (h *httpWatchHandle) KVScope() contract.KVScope { return h.scope.Clone() }
func (h *httpWatchHandle) ReserveKVHTTPWatch(id uint64) (contract.KVHTTPWatchLease, error) {
	return h.reserve(id)
}
func (h *httpWatchHandle) RecoverKVHTTPWatch(l contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
	return h.recover(l)
}
func httpWatchHandleFor(id int64) *httpWatchHandle {
	h := kvHandleFor(id)
	h.scope.Header = contract.KVHeader{}
	h.scope.HTTP = contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "https://original", Configuration: "original", AgentID: "original-agent", AgentAvailable: true}
	return &httpWatchHandle{handle: h.handle, scope: h.scope}
}

type httpWatchFakePreparation struct{ request contract.KVHTTPWatchRequest }

func (p *httpWatchFakePreparation) Snapshot() contract.KVHTTPWatchRequest { return p.request }

type httpWatchFakeLease struct {
	scope   contract.KVScope
	closed  atomic.Int32
	prepare func(contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error)
	start   func(contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error)
	last    *contract.KVHTTPWatchSnapshot
}

func (l *httpWatchFakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *httpWatchFakeLease) CheckKVLease() error {
	if l.closed.Load() > 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (l *httpWatchFakeLease) Limits() contract.KVHTTPWatchLimits {
	return contract.KVHTTPWatchLimits{MaxObservations: 256}
}
func (l *httpWatchFakeLease) Prepare(r contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error) {
	if l.prepare != nil {
		return l.prepare(r)
	}
	return &httpWatchFakePreparation{r}, nil
}
func (l *httpWatchFakeLease) Start(_ context.Context, p contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
	return l.start(p)
}
func (l *httpWatchFakeLease) LastWatch() *contract.KVHTTPWatchSnapshot { return l.last }
func (l *httpWatchFakeLease) ClearObservations() error                 { l.last = nil; return nil }
func (l *httpWatchFakeLease) Close() error                             { l.closed.Add(1); return nil }

type httpWatchFakeRun struct {
	request  contract.KVHTTPWatchRequest
	snapshot contract.KVHTTPWatchSnapshot
	closed   atomic.Int32
}

func (w *httpWatchFakeRun) Request() contract.KVHTTPWatchRequest   { return w.request }
func (w *httpWatchFakeRun) Snapshot() contract.KVHTTPWatchSnapshot { return w.snapshot }
func (w *httpWatchFakeRun) Poll() contract.KVHTTPWatchBatch        { return w.snapshot.KVHTTPWatchBatch }
func (w *httpWatchFakeRun) Stop(context.Context) error             { return nil }
func (w *httpWatchFakeRun) Close() error                           { w.closed.Add(1); return nil }

func TestHTTPWatchRuntimeLateOriginalRunAndImmutableReceiptsSurviveReplacement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var starts, recoveries atomic.Int32
	var originalRun *httpWatchFakeRun
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := httpWatchHandleFor(p.ID)
		h.reserve = func(uint64) (contract.KVHTTPWatchLease, error) {
			l := &httpWatchFakeLease{scope: h.scope.Clone()}
			l.start = func(p contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
				starts.Add(1)
				close(entered)
				<-release
				index := uint64(math.MaxUint64)
				originalRun = &httpWatchFakeRun{request: p.Snapshot(), snapshot: contract.KVHTTPWatchSnapshot{Request: p.Snapshot(), KVHTTPWatchBatch: contract.KVHTTPWatchBatch{Ended: true, Observations: []contract.KVHTTPWatchObservation{{HTTP: contract.KVHTTPResult{Body: []byte{0, 255}, Metadata: contract.KVHTTPMetadata{Index: &index}}}}}}}
				return originalRun, nil
			}
			return l, nil
		}
		h.recover = func(old contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
			recoveries.Add(1)
			v := originalRun.Snapshot()
			return contract.KVHTTPWatchRecovery{Lease: &httpWatchFakeLease{scope: h.scope.Clone()}, Watch: &v}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ := m.KVHTTPWatch(1)
	l, _ := p.ReserveKVHTTPWatch(1)
	defer l.Close()
	request := contract.KVHTTPWatchRequest{Target: "original//key", ObservationPrefix: "original", StartIndex: math.MaxUint64, MaxObservations: 1}
	prepared, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		w   contract.KVHTTPWatchRun
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
	fresh, _ := m.KVHTTPWatch(1)
	recovery, err := fresh.RecoverKVHTTPWatch(l)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation <= l.Scope().Generation || recoveries.Load() != 1 || starts.Load() != 1 {
		t.Fatal("recovery resumed/substituted Watch", err)
	}
	defer recovery.Lease.Close()
}

func TestHTTPWatchRuntimeRejectsChangedPreparationAndForeignRun(t *testing.T) {
	for _, mode := range []string{"prepare", "run", "foreign-plan"} {
		t.Run(mode, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := httpWatchHandleFor(1)
			var started atomic.Int32
			var rawRun *httpWatchFakeRun
			h.reserve = func(uint64) (contract.KVHTTPWatchLease, error) {
				l := &httpWatchFakeLease{scope: h.scope.Clone()}
				l.prepare = func(r contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error) {
					if mode == "prepare" {
						r.ObservationPrefix = ""
					}
					return &httpWatchFakePreparation{r}, nil
				}
				l.start = func(p contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
					started.Add(1)
					r := p.Snapshot()
					if mode == "run" {
						r.Target = "foreign"
					}
					rawRun = &httpWatchFakeRun{request: r}
					return rawRun, nil
				}
				return l, nil
			}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVHTTPWatch(1)
			l, _ := provider.ReserveKVHTTPWatch(1)
			defer l.Close()
			r := contract.KVHTTPWatchRequest{Target: "original/key", ObservationPrefix: "original"}
			p, err := l.Prepare(r)
			if mode == "prepare" {
				if !errors.Is(err, contract.ErrRevoked) || started.Load() != 0 {
					t.Fatal("altered range authorized")
				}
				return
			}
			if mode == "foreign-plan" {
				other, _ := provider.ReserveKVHTTPWatch(2)
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

func TestHTTPWatchRuntimeForeignScopeRejectedBeforeRecovery(t *testing.T) {
	for _, change := range []func(*contract.KVScope){
		func(s *contract.KVScope) { s.HTTP.Endpoint = "https://foreign" }, func(s *contract.KVScope) { s.HTTP.AgentID = "foreign" }, func(s *contract.KVScope) { s.HTTP.Configuration = "foreign" }, func(s *contract.KVScope) { s.Prefix = []byte("foreign") }, func(s *contract.KVScope) { s.Header.ClusterID = 1 },
	} {
		m := dbruntime.New()
		var opens, recoveries atomic.Int32
		var oldRaw *httpWatchFakeLease
		m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
			h := httpWatchHandleFor(1)
			if opens.Add(1) > 1 {
				change(&h.scope)
			}
			h.reserve = func(uint64) (contract.KVHTTPWatchLease, error) {
				l := &httpWatchFakeLease{scope: h.scope.Clone()}
				oldRaw = l
				return l, nil
			}
			h.recover = func(contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
				recoveries.Add(1)
				return contract.KVHTTPWatchRecovery{}, nil
			}
			return h, nil
		}))
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		p, _ := m.KVHTTPWatch(1)
		old, _ := p.ReserveKVHTTPWatch(1)
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		fresh, _ := m.KVHTTPWatch(1)
		if _, err := fresh.RecoverKVHTTPWatch(old); !errors.Is(err, contract.ErrRevoked) || recoveries.Load() != 0 || oldRaw.closed.Load() != 0 {
			t.Fatal("foreign identity altered original evidence", err)
		}
		old.Close()
		m.Close()
	}
}
