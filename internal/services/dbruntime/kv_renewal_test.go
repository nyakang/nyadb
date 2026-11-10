package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type renewalHandle struct {
	*handle
	scope   contract.KVScope
	reserve func(uint64) (contract.KVRenewalLease, error)
	recover func(contract.KVRenewalLease) (contract.KVRenewalRecovery, error)
}

func (h *renewalHandle) KVScope() contract.KVScope { return h.scope.Clone() }
func (h *renewalHandle) ReserveKVRenewal(id uint64) (contract.KVRenewalLease, error) {
	return h.reserve(id)
}
func (h *renewalHandle) RecoverKVRenewal(l contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
	return h.recover(l)
}

type renewalFakePreparation struct{ request contract.KVRenewalRequest }

func (p *renewalFakePreparation) Snapshot() contract.KVRenewalRequest { return p.request }

type renewalFakeLease struct {
	scope   contract.KVScope
	closed  atomic.Int32
	prepare func(contract.KVRenewalRequest) (contract.KVRenewalPreparation, error)
	start   func(contract.KVRenewalPreparation) (contract.KVRenewalRun, error)
}

func (l *renewalFakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *renewalFakeLease) CheckKVLease() error {
	if l.closed.Load() > 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (l *renewalFakeLease) Prepare(r contract.KVRenewalRequest) (contract.KVRenewalPreparation, error) {
	if l.prepare != nil {
		return l.prepare(r)
	}
	return &renewalFakePreparation{r}, nil
}
func (l *renewalFakeLease) Preconnect(context.Context) error { return nil }
func (l *renewalFakeLease) Start(_ context.Context, p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
	return l.start(p)
}
func (l *renewalFakeLease) ClearReceipts() error { return nil }
func (l *renewalFakeLease) Close() error         { l.closed.Add(1); return nil }

type renewalFakeRun struct {
	request         contract.KVRenewalRequest
	batch           contract.KVRenewalBatch
	closed, stopped atomic.Int32
}

func (r *renewalFakeRun) Request() contract.KVRenewalRequest { return r.request }
func (r *renewalFakeRun) Poll() contract.KVRenewalBatch      { return r.batch }
func (r *renewalFakeRun) Stop(context.Context) error         { r.stopped.Add(1); return nil }
func (r *renewalFakeRun) Close() error                       { r.closed.Add(1); return nil }
func TestKVRenewalRuntimeLateOriginalFirstReceiptAndRecoveryNeverRestarts(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var starts, recoveries atomic.Int32
	var leases []*renewalFakeLease
	var rawRun *renewalFakeRun
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &renewalHandle{handle: &handle{id: p.ID}, scope: kvHandleFor(p.ID).scope}
		h.reserve = func(uint64) (contract.KVRenewalLease, error) {
			l := &renewalFakeLease{scope: h.scope.Clone()}
			l.start = func(p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
				starts.Add(1)
				close(entered)
				<-release
				rawRun = &renewalFakeRun{request: p.Snapshot(), batch: contract.KVRenewalBatch{Request: p.Snapshot(), Receipts: []contract.KVRenewalReceipt{{Native: []byte{0, 255}, WriteOutcomeUnknown: true}}, Ended: true, Terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true}}}
				return rawRun, nil
			}
			leases = append(leases, l)
			return l, nil
		}
		h.recover = func(old contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
			recoveries.Add(1)
			old.Close()
			fresh, _ := h.reserve(1)
			return contract.KVRenewalRecovery{Lease: fresh, Batch: rawRun.batch}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRenewal(1)
	old, err := provider.ReserveKVRenewal(1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := old.Prepare(contract.KVRenewalRequest{LeaseID: 9223372036854775807})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		run contract.KVRenewalRun
		err error
	}
	out := make(chan outcome, 1)
	go func() { r, err := old.Start(context.Background(), p); out <- outcome{r, err} }()
	kvAwait(t, entered)
	if err = m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-out
	if !errors.Is(result.err, contract.ErrRevoked) || result.run == nil || result.run.Request() != p.Snapshot() {
		t.Fatal("late original receipt discarded/substituted", result.err)
	}
	batch := result.run.Poll()
	batch.Receipts[0].Native[0] = 9
	if rawRun.batch.Receipts[0].Native[0] != 0 || !batch.Terminal.WriteOutcomeUnknown {
		t.Fatal("original receipt aliased/reclassified")
	}
	if err = result.run.Stop(context.Background()); err != nil || rawRun.stopped.Load() != 1 {
		t.Fatal("retired original cannot settle", err)
	}
	freshProvider, _ := m.KVRenewal(1)
	recovered, err := freshProvider.RecoverKVRenewal(old)
	if err != nil || recovered.Lease == nil || recovered.Lease.Scope().Generation <= old.Scope().Generation || !recovered.Batch.Terminal.WriteOutcomeUnknown || starts.Load() != 1 || recoveries.Load() != 1 {
		t.Fatal("recovery restarted/reclassified original", err)
	}
	recovered.Batch.Receipts[0].Native[0] = 7
	if rawRun.batch.Receipts[0].Native[0] != 0 || leases[0].closed.Load() != 1 || leases[1].closed.Load() != 0 {
		t.Fatal("recovery aliases or closes replacement")
	}
	deferred := recovered.Lease
	if err = deferred.Close(); err != nil {
		t.Fatal(err)
	}
	if err = deferred.Close(); err != nil || leases[1].closed.Load() != 1 {
		t.Fatal("lease close not idempotent", err)
	}
}
func TestKVRenewalRuntimeRejectsForeignScopePlansAndExecutorRequests(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &renewalHandle{handle: &handle{id: 1}, scope: kvHandleFor(1).scope}
	var l *renewalFakeLease
	var original *renewalFakeRun
	h.reserve = func(uint64) (contract.KVRenewalLease, error) {
		l = &renewalFakeLease{scope: h.scope.Clone()}
		l.start = func(p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
			original = &renewalFakeRun{request: contract.KVRenewalRequest{LeaseID: p.Snapshot().LeaseID + 1}}
			return original, nil
		}
		return l, nil
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRenewal(1)
	first, _ := provider.ReserveKVRenewal(1)
	second, _ := provider.ReserveKVRenewal(2)
	p, _ := first.Prepare(contract.KVRenewalRequest{LeaseID: 7})
	if _, err := second.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign plan authorized", err)
	}
	if _, err := first.Start(context.Background(), p); !errors.Is(err, contract.ErrRevoked) || original.closed.Load() != 1 {
		t.Fatal("foreign executor request admitted", err)
	}
	l.prepare = func(r contract.KVRenewalRequest) (contract.KVRenewalPreparation, error) {
		return &renewalFakePreparation{contract.KVRenewalRequest{LeaseID: r.LeaseID + 1}}, nil
	}
	if _, err := second.Prepare(contract.KVRenewalRequest{LeaseID: 7}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("foreign preparation admitted", err)
	}
	h.reserve = func(uint64) (contract.KVRenewalLease, error) {
		l = &renewalFakeLease{scope: h.scope.Clone()}
		l.scope.Generation++
		return l, nil
	}
	if _, err := provider.ReserveKVRenewal(3); !errors.Is(err, contract.ErrRevoked) || l.closed.Load() != 1 {
		t.Fatal("foreign raw generation admitted", err)
	}
}
