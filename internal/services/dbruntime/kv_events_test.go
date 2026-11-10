package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type eventsHandle struct {
	*handle
	scope   contract.KVScope
	reserve func(uint64) (contract.KVEventsLease, error)
	recover func(contract.KVEventsLease) (contract.KVEventsRecovery, error)
}

func (h *eventsHandle) KVScope() contract.KVScope { return h.scope.Clone() }
func (h *eventsHandle) ReserveKVEvents(id uint64) (contract.KVEventsLease, error) {
	return h.reserve(id)
}
func (h *eventsHandle) RecoverKVEvents(old contract.KVEventsLease) (contract.KVEventsRecovery, error) {
	return h.recover(old)
}

type eventsFakeLease struct {
	scope   contract.KVScope
	closed  atomic.Int32
	start   func(contract.KVWatchRequest) (contract.KVWatchRun, error)
	history func(contract.KVHistoryRequest) (contract.KVHistoryResult, error)
}

func (l *eventsFakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *eventsFakeLease) CheckKVLease() error {
	if l.closed.Load() > 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (*eventsFakeLease) Preconnect(context.Context) error { return nil }
func (l *eventsFakeLease) StartWatch(_ context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
	return l.start(r)
}
func (l *eventsFakeLease) History(_ context.Context, r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
	return l.history(r)
}
func (*eventsFakeLease) LastHistory() *contract.KVHistoryResult { return nil }
func (*eventsFakeLease) ClearReceipts() error                   { return nil }
func (l *eventsFakeLease) Close() error                         { l.closed.Add(1); return nil }

type eventsFakeRun struct {
	request contract.KVWatchRequest
	batch   contract.KVWatchBatch
	closed  atomic.Int32
}

func (w *eventsFakeRun) Request() contract.KVWatchRequest { return w.request }
func (w *eventsFakeRun) Poll() contract.KVWatchBatch      { return w.batch }
func (*eventsFakeRun) Progress() error                    { return nil }
func (*eventsFakeRun) Stop(context.Context) error         { return nil }
func (w *eventsFakeRun) Close() error                     { w.closed.Add(1); return nil }

func eventHandleFor(id int64) *eventsHandle {
	return &eventsHandle{handle: &handle{id: id}, scope: kvHandleFor(id).scope}
}

func TestKVEventsPublicationPreservesLateOriginalHistoryAndRequiresExplicitRecovery(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var histories, recoveries atomic.Int32
	var leases []*eventsFakeLease
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := eventHandleFor(p.ID)
		h.reserve = func(uint64) (contract.KVEventsLease, error) {
			l := &eventsFakeLease{scope: h.scope.Clone()}
			l.history = func(r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
				histories.Add(1)
				close(entered)
				<-release
				return contract.KVHistoryResult{Authored: r.Clone(), Window: r.Clone(), Initial: contract.KVResult{Native: []byte{0, 255}}, Frames: []contract.KVEventFrame{{Native: []byte{7}}}}, nil
			}
			leases = append(leases, l)
			return l, nil
		}
		h.recover = func(old contract.KVEventsLease) (contract.KVEventsRecovery, error) {
			recoveries.Add(1)
			_ = old.Close()
			fresh, _ := h.reserve(1)
			return contract.KVEventsRecovery{Lease: fresh, History: &contract.KVHistoryResult{Initial: contract.KVResult{Native: []byte{9}}}}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	old, err := provider.ReserveKVEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result contract.KVHistoryResult
		err    error
	}
	out := make(chan outcome, 1)
	request := contract.KVHistoryRequest{Key: []byte{0, 255}, StartRevision: 3, EndRevision: 7, Limit: 100}
	go func() { r, err := old.History(context.Background(), request); out <- outcome{r, err} }()
	kvAwait(t, entered)
	if err = m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	completed := <-out
	if !errors.Is(completed.err, contract.ErrRevoked) || !bytes.Equal(completed.result.Initial.Native, []byte{0, 255}) || !bytes.Equal(completed.result.Authored.Key, request.Key) || len(completed.result.Frames) != 1 {
		t.Fatal("publication replacement erased/substituted original history receipt", completed.err)
	}
	freshProvider, _ := m.KVEvents(1)
	recovery, err := freshProvider.RecoverKVEvents(old)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation <= old.Scope().Generation || histories.Load() != 1 || recoveries.Load() != 1 || !bytes.Equal(recovery.History.Initial.Native, []byte{9}) {
		t.Fatal("recovery replayed or lost original evidence", err)
	}
	if leases[0].closed.Load() != 1 || leases[1].closed.Load() != 0 {
		t.Fatal("recovery closed the replacement lease")
	}
}

func TestKVEventsWatchSnapshotsAndForeignOriginalRequests(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	raw := eventHandleFor(1)
	var lease *eventsFakeLease
	var original *eventsFakeRun
	raw.reserve = func(uint64) (contract.KVEventsLease, error) {
		lease = &eventsFakeLease{scope: raw.scope.Clone()}
		lease.start = func(r contract.KVWatchRequest) (contract.KVWatchRun, error) {
			original = &eventsFakeRun{request: r.Clone(), batch: contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: []byte{7}}}}}
			return original, nil
		}
		return lease, nil
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return raw, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ := m.KVEvents(1)
	l, err := p.ReserveKVEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	r := contract.KVWatchRequest{Key: []byte{0, 255}, WatchID: 1, PrevKV: true}
	w, err := l.StartWatch(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Key[0] = 9
	if w.Request().Key[0] != 0 {
		t.Fatal("runtime Watch aliases authored input")
	}
	b := w.Poll()
	b.Frames[0].Native[0] = 9
	if original.batch.Frames[0].Native[0] != 7 {
		t.Fatal("runtime Poll aliases original receipt")
	}
	lease.start = func(r contract.KVWatchRequest) (contract.KVWatchRun, error) {
		original = &eventsFakeRun{request: r.Clone()}
		original.request.Key[0] = 42
		return original, nil
	}
	if _, err = l.StartWatch(context.Background(), contract.KVWatchRequest{Key: []byte{0, 255}, WatchID: 2}); !errors.Is(err, contract.ErrRevoked) || original.closed.Load() != 1 {
		t.Fatal("foreign original Watch request admitted", err)
	}
}
