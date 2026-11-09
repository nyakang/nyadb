package dbruntime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type kvHandle struct {
	*handle
	scope   contract.KVScope
	reserve func(uint64) (contract.KVRequestLease, error)
	recover func(contract.KVRequestLease) (contract.KVRecovery, error)
}

func (h *kvHandle) KVScope() contract.KVScope                            { return h.scope.Clone() }
func (h *kvHandle) ReserveKV(id uint64) (contract.KVRequestLease, error) { return h.reserve(id) }
func (h *kvHandle) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	return h.recover(l)
}

type kvFakePlan struct{ request contract.KVRequest }

func (p *kvFakePlan) Snapshot() contract.KVRequest { return p.request }
func (*kvFakePlan) Writes() bool                   { return true }

type kvFakeLease struct {
	scope    contract.KVScope
	closed   atomic.Int32
	executes atomic.Int32
	execute  func(context.Context, contract.KVPreparation) (contract.KVResult, error)
}

func (l *kvFakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *kvFakeLease) CheckKVLease() error {
	if l.closed.Load() > 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (l *kvFakeLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	return &kvFakePlan{r}, nil
}
func (*kvFakeLease) Preconnect(context.Context) error { return nil }
func (l *kvFakeLease) Execute(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
	l.executes.Add(1)
	if l.execute != nil {
		return l.execute(ctx, p)
	}
	return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true}, nil
}
func (*kvFakeLease) LastResult() contract.KVResult { return contract.KVResult{} }
func (l *kvFakeLease) Close() error                { l.closed.Add(1); return nil }
func kvHandleFor(id int64) *kvHandle {
	return &kvHandle{handle: &handle{id: id}, scope: contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: id, Catalog: "kv"}, Generation: 1, Prefix: []byte{0, 255}, Header: contract.KVHeader{ClusterID: 3, MemberID: 4, Revision: 1}, Version: "3.7.3"}}
}
func kvAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("original KV executor did not settle")
	}
}
func TestKVPublishedGenerationForeignPlansAndRecovery(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var raw []*kvHandle
	var leases []*kvFakeLease
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := kvHandleFor(p.ID)
		h.reserve = func(uint64) (contract.KVRequestLease, error) {
			l := &kvFakeLease{scope: h.scope.Clone()}
			leases = append(leases, l)
			return l, nil
		}
		h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
			_ = old.Close()
			fresh, _ := h.reserve(1)
			return contract.KVRecovery{Lease: fresh, Last: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{1, 2}}}, nil
		}
		raw = append(raw, h)
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	first, _ := m.KVRequests(1)
	old, _ := first.ReserveKV(1)
	plan, _ := old.Prepare(contract.KVRequest{Method: "Put", JSON: "original"})
	foreign, _ := first.ReserveKV(2)
	if _, err := foreign.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) || leases[1].executes.Load() != 0 {
		t.Fatal("foreign plan dispatched")
	}
	snapshot := old.Scope()
	snapshot.Prefix[0] = 9
	if old.Scope().Prefix[0] != 0 {
		t.Fatal("scope aliases runtime")
	}
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	freshProvider, _ := m.KVRequests(1)
	if freshProvider.KVScope().Generation <= old.Scope().Generation || raw[1].Generation() != 1 {
		t.Fatal("contextual generation leaked into publication")
	}
	if _, err := old.Execute(context.Background(), plan); !errors.Is(err, contract.ErrRevoked) || leases[0].executes.Load() != 0 {
		t.Fatal("revoked original dispatched")
	}
	recovery, err := freshProvider.RecoverKV(old)
	if err != nil || !recovery.Last.WriteOutcomeUnknown || recovery.Lease.Scope().Generation != m.Generation(1) {
		t.Fatal("original evidence/generation lost", err)
	}
	if _, err = recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery replayed original plan")
	}
	incompatible, _ := freshProvider.ReserveKV(9)
	raw[1].scope.Prefix = []byte("other")
	if _, err = freshProvider.RecoverKV(incompatible); !errors.Is(err, contract.ErrRevoked) || leases[3].closed.Load() != 0 {
		t.Fatal("incompatible recovery closed original")
	}
}
func TestKVLateReservationClosesCapturedLease(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	original := kvHandleFor(1)
	late := &kvFakeLease{scope: original.scope.Clone()}
	original.reserve = func(uint64) (contract.KVRequestLease, error) { close(started); <-release; return late, nil }
	replacement := kvHandleFor(1)
	newLease := &kvFakeLease{scope: replacement.scope.Clone()}
	replacement.reserve = func(uint64) (contract.KVRequestLease, error) { return newLease, nil }
	var calls atomic.Int32
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		if calls.Add(1) == 1 {
			return original, nil
		}
		return replacement, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(1)
	done := make(chan error, 1)
	go func() { _, err := provider.ReserveKV(1); done <- err }()
	kvAwait(t, started)
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, contract.ErrRevoked) || late.closed.Load() != 1 || newLease.closed.Load() != 0 {
		t.Fatal("late reservation damaged replacement", err)
	}
}
func TestKVActualOriginalCompletionSurvivesPublicationRevocation(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := kvHandleFor(1)
	native := []byte{1, 2, 3}
	reply := &contract.KVReply{Entries: []contract.KVEntry{{Value: []byte("original")}}}
	l := &kvFakeLease{scope: h.scope.Clone(), execute: func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		close(started)
		<-release
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Native: native, Reply: reply}, nil
	}}
	h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(1)
	lease, _ := provider.ReserveKV(1)
	plan, _ := lease.Prepare(contract.KVRequest{JSON: "original"})
	type completion struct {
		r   contract.KVResult
		err error
	}
	done := make(chan completion, 1)
	go func() { r, err := lease.Execute(context.Background(), plan); done <- completion{r, err} }()
	kvAwait(t, started)
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	result := <-done
	if !errors.Is(result.err, contract.ErrRevoked) || !result.r.Completed || result.r.WriteOutcomeUnknown || result.r.Request != plan.Snapshot() {
		t.Fatal("actual original completion erased", result.err)
	}
	result.r.Native[0] = 9
	result.r.Reply.Entries[0].Value[0] = 'x'
	if native[0] != 1 || string(reply.Entries[0].Value) != "original" {
		t.Fatal("parked result aliases adapter")
	}
}
