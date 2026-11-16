package zookeeper

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

type fakeOriginal struct {
	alive atomic.Bool
	calls atomic.Int32
	call  func(context.Context, string, any) (agentbridge.Receipt, error)
}

func fakeProcess() *fakeOriginal     { p := &fakeOriginal{}; p.alive.Store(true); return p }
func (p *fakeOriginal) Alive() bool  { return p.alive.Load() }
func (p *fakeOriginal) Close() error { p.alive.Store(false); return nil }
func (p *fakeOriginal) OpeningReceipts() []contract.KVResult {
	return []contract.KVResult{{ZNode: &contract.KVZNodeResult{Reply: &contract.KVZNodeReply{Kind: "connection_info", ProductName: "ZooKeeper", ProductVersion: "3.9.4"}}}}
}
func (p *fakeOriginal) Call(ctx context.Context, m string, params any) (agentbridge.Receipt, error) {
	p.calls.Add(1)
	if p.call != nil {
		return p.call(ctx, m, params)
	}
	return agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, Result: json.RawMessage(`{"found":false,"key":"/x","value":null,"metadata":null}`)}, nil
}

func testSession(t *testing.T) (*Session, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	opens := &atomic.Int32{}
	s := &Session{id: 7, root: fakeProcess(), ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), options: Options{MaxDocuments: 8}, leases: map[*documentLease]bool{}, scope: contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: 7}, Generation: 1, Prefix: []byte("/"), Version: "3.9.4", ZNode: contract.KVZNodeIdentity{Protocol: nativeProtocol, Root: "/", Configuration: "original"}}}
	s.opener = func(context.Context) (originalRuntime, error) { opens.Add(1); return fakeProcess(), nil }
	t.Cleanup(func() {
		s.Close()
		s.mu.Lock()
		var leases []*documentLease
		for l := range s.leases {
			leases = append(leases, l)
		}
		s.mu.Unlock()
		for _, l := range leases {
			l.Close()
		}
	})
	return s, opens
}
func reserve(t *testing.T, s *Session, id uint64) *documentLease {
	t.Helper()
	l, err := s.ReserveKV(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.(*documentLease)
}
func plan(t *testing.T, l contract.KVRequestLease, m, body string) contract.KVPreparation {
	t.Helper()
	p, err := l.Prepare(contract.KVRequest{Method: m, JSON: body})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func waitNative(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor barrier not observed")
	}
}

func TestDocumentReservationPreparationAndOneUseRemainIndependent(t *testing.T) {
	s, opens := testSession(t)
	first, other := reserve(t, s, 1), reserve(t, s, 2)
	p := plan(t, first, "kv_get", `{"key":"/x"}`)
	if opens.Load() != 0 {
		t.Fatal("pure reserve/prepare opened a process")
	}
	if _, err := other.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("plan changed its original document", err)
	}
	if r, err := first.Execute(context.Background(), p); err != nil || !r.Completed {
		t.Fatal(err)
	}
	if _, err := first.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original plan replayed", err)
	}
	if err := other.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 2 || first.physical == other.physical {
		t.Fatal("documents share an original process")
	}
	if _, err := s.ReserveKV(1); err == nil {
		t.Fatal("duplicate document identity admitted")
	}
	first.Close()
	if !other.physical.Alive() {
		t.Fatal("close reached another original document")
	}
}

func TestOriginalCancellationWaitsExecutorAndRetainsUnknownAcrossReadsAndRecovery(t *testing.T) {
	s, opens := testSession(t)
	entered, released, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := fakeProcess()
	p.call = func(ctx context.Context, _ string, _ any) (agentbridge.Receipt, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-released
		p.alive.Store(false)
		return agentbridge.Receipt{Dispatched: true, CancellationRequested: true, ProcessExitConfirmed: true}, ctx.Err()
	}
	s.opener = func(context.Context) (originalRuntime, error) { opens.Add(1); return p, nil }
	l := reserve(t, s, 1)
	request := plan(t, l, "kv_put", `{"key":"/x","value":{"data":"original"}}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan contract.KVResult, 1)
	go func() { r, _ := l.Execute(ctx, request); done <- r }()
	waitNative(t, entered)
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queuedCancel()
	queued := plan(t, l, "kv_get", `{"key":"/x"}`)
	if r, err := l.Execute(queuedCtx, queued); !errors.Is(err, context.Canceled) || r.Attempted {
		t.Fatal("queued cancellation dispatched", err)
	}
	cancel()
	waitNative(t, canceled)
	select {
	case <-done:
		t.Fatal("cancel intent replaced original executor settlement")
	default:
	}
	close(released)
	r := <-done
	if !r.WriteOutcomeUnknown || !r.ZNode.ProcessExitConfirmed || r.ZNode.Acknowledged {
		t.Fatal("unknown write was erased")
	}
	s.opener = func(context.Context) (originalRuntime, error) { opens.Add(1); return fakeProcess(), nil }
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Unknown == nil || !recovery.Unknown.WriteOutcomeUnknown || opens.Load() != 1 {
		t.Fatal("recovery opened/replayed or lost unknown evidence", err)
	}
	next := recovery.Lease
	t.Cleanup(func() { next.Close() })
	read := plan(t, next, "kv_get", `{"key":"/x"}`)
	if _, err := next.Execute(context.Background(), read); err != nil {
		t.Fatal(err)
	}
	write := plan(t, next, "kv_put", `{"key":"/y","value":{"data":"later"}}`)
	if _, err := next.Execute(context.Background(), write); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("later read unlocked original unknown write", err)
	}
	second, err := s.RecoverKV(next)
	if err != nil || second.Unknown == nil || second.Unknown.Request != r.Request || !second.Last.Completed {
		t.Fatal("second recovery lost independent receipts", err)
	}
	second.Lease.Close()
}

func TestSessionCloseWaitsOriginalWriteAndLeavesEvidenceUntilDocumentDisposal(t *testing.T) {
	s, _ := testSession(t)
	entered, released, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := fakeProcess()
	p.call = func(ctx context.Context, _ string, _ any) (agentbridge.Receipt, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-released
		return agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, Result: json.RawMessage(`{"version":0,"mtime":123}`)}, ctx.Err()
	}
	s.opener = func(context.Context) (originalRuntime, error) { return p, nil }
	l := reserve(t, s, 1)
	prepared := plan(t, l, "kv_put", `{"key":"/x","value":{"data":"value"}}`)
	done := make(chan contract.KVResult, 1)
	go func() { r, _ := l.Execute(context.Background(), prepared); done <- r }()
	waitNative(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	waitNative(t, canceled)
	select {
	case <-closed:
		t.Fatal("session close returned before original executor")
	default:
	}
	close(released)
	r := <-done
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !r.Completed || r.WriteOutcomeUnknown || !l.LastResult().Completed {
		t.Fatal("original acknowledged write became uncertainty")
	}
	if _, err := s.ReserveKV(2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed session reserved a document")
	}
	l.Close()
	if l.LastResult().Attempted || s.retained != 0 || s.reserved != 0 {
		t.Fatal("document disposal leaked original evidence accounting")
	}
}

func TestRecoveryRejectsForeignIdentityAndBudgetFailureKeepsOriginalEvidence(t *testing.T) {
	s, _ := testSession(t)
	l := reserve(t, s, 1)
	p := fakeProcess()
	p.call = func(context.Context, string, any) (agentbridge.Receipt, error) {
		return agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, RemoteError: &agentbridge.RemoteError{Code: -1}}, &agentbridge.RemoteError{Code: -1}
	}
	s.opener = func(context.Context) (originalRuntime, error) { return p, nil }
	_, _ = l.Execute(context.Background(), plan(t, l, "kv_put", `{"key":"/x","value":{"data":"unknown"}}`))
	next, opens := testSession(t)
	next.scope.ZNode.Root = "/other"
	if _, err := next.RecoverKV(l); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("recovery changed original root")
	}
	next.scope = s.scope.Clone()
	receiptBudgetMu.Lock()
	next.reserved = sessionBudget
	receiptBudgetMu.Unlock()
	if r, err := next.RecoverKV(l); !errors.Is(err, ErrBounds) || r.Unknown == nil || l.unknown == nil || opens.Load() != 0 {
		t.Fatal("budget failure discarded evidence or opened a process", err)
	}
	receiptBudgetMu.Lock()
	next.reserved = 0
	receiptBudgetMu.Unlock()
	r, err := next.RecoverKV(l)
	if err != nil || r.Unknown == nil || r.Lease == nil || l.unknown != nil {
		t.Fatal("original evidence did not transfer after budget became available", err)
	}
	r.Lease.Close()
}
