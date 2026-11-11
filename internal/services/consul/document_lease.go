package consul

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type documentPreparation struct {
	owner   *documentLease
	native  Request
	request contract.KVRequest
	write   bool
	used    atomic.Bool
}

func (p *documentPreparation) Snapshot() contract.KVRequest { return p.request }
func (p *documentPreparation) Writes() bool                 { return p.write }

type documentLease struct {
	s                          *Session
	document                   uint64
	mu                         sync.Mutex
	receiptMu                  sync.Mutex
	ops                        sync.Mutex
	ctx                        context.Context
	cancel                     context.CancelFunc
	closed, retired, recovered bool
	client                     *Client            // guarded by ops
	last, unknown              *contract.KVResult // immutable, guarded by receiptMu
	writeUnknown               atomic.Bool
	reserved                   bool  // guarded by ops and receiptBudgetMu
	cacheBytes                 int64 // guarded by receiptBudgetMu and ops
}

func (l *documentLease) Scope() contract.KVScope { return l.s.KVScope() }
func (l *documentLease) CheckKVLease() error {
	l.mu.Lock()
	closed, retired, recovered := l.closed, l.retired, l.recovered
	l.mu.Unlock()
	if closed || recovered {
		return contract.ErrRevoked
	}
	if retired {
		return contract.ErrKVLeaseLost
	}
	if !l.s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *documentLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	native, canonical, write, err := decodeDocumentRequest(r)
	if err != nil {
		return nil, err
	}
	if write && l.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	// Prepare never waits on the executor and never grants I/O authority.
	return &documentPreparation{owner: l, native: native.Clone(), request: canonical, write: write}, nil
}
func (l *documentLease) request(ctx context.Context) (context.Context, func(), error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, nil, err
	}
	parent, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *documentLease) connect(ctx context.Context) error {
	if l.client != nil {
		if !l.client.Alive() {
			return l.markLost(ErrRetired)
		}
		return nil
	}
	c, err := l.s.open(ctx)
	if err != nil {
		return l.markLost(err)
	}
	l.client = c
	return nil
}
func (l *documentLease) markLost(err error) error {
	l.mu.Lock()
	l.retired = true
	l.mu.Unlock()
	l.closePhysical()
	return errors.Join(contract.ErrKVLeaseLost, err)
}
func (l *documentLease) closePhysical() {
	if l.client != nil {
		l.client.Close()
		l.client = nil
	}
}
func (l *documentLease) reserveReceipt() error {
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	if l.cacheBytes+pendingReceiptBudget > documentReceiptBudget || l.s.retained+l.s.reserved+pendingReceiptBudget > l.s.budget {
		return ErrBounds
	}
	l.s.reserved += pendingReceiptBudget
	l.reserved = true
	return nil
}
func (l *documentLease) releaseReservation() {
	receiptBudgetMu.Lock()
	if l.reserved {
		l.s.reserved -= pendingReceiptBudget
		l.reserved = false
	}
	receiptBudgetMu.Unlock()
}
func (l *documentLease) Preconnect(parent context.Context) error {
	if !l.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return err
	}
	defer done()
	if err = l.reserveReceipt(); err != nil {
		return err
	}
	defer l.releaseReservation()
	return l.connect(ctx)
}
func (l *documentLease) Execute(parent context.Context, prepared contract.KVPreparation) (result contract.KVResult, err error) {
	p, ok := prepared.(*documentPreparation)
	if !ok || p == nil || p.owner != l {
		return result, contract.ErrKVPlanUsed
	}
	result.Request = p.request
	if !l.ops.TryLock() {
		return result, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return result, err
	}
	defer done()
	if p.used.Load() {
		return result, contract.ErrKVPlanUsed
	}
	if p.write && l.writeUnknown.Load() {
		return result, contract.ErrKVWriteUnknown
	}
	if err = l.reserveReceipt(); err != nil {
		return result, err
	}
	defer l.releaseReservation()
	if !p.used.CompareAndSwap(false, true) {
		return result, contract.ErrKVPlanUsed
	}
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	native, err := l.client.Prepare(p.native)
	if err != nil {
		return result, err
	}
	r, err := l.client.Execute(ctx, native)
	result = documentResult(p.request, r)
	if result.WriteOutcomeUnknown {
		l.writeUnknown.Store(true)
	}
	l.storeOwned(result)
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	return result, err
}
func (l *documentLease) storeOwned(result contract.KVResult) {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	v := result.Clone()
	next := &v
	unknown := l.unknown
	if result.WriteOutcomeUnknown {
		unknown = next
	}
	bytes := resultCost(next)
	if unknown != nil && unknown != next {
		bytes += resultCost(unknown)
	}
	receiptBudgetMu.Lock()
	if l.reserved {
		l.s.reserved -= pendingReceiptBudget
		l.reserved = false
	}
	l.s.retained += bytes - l.cacheBytes
	l.cacheBytes = bytes
	receiptBudgetMu.Unlock()
	l.last, l.unknown = next, unknown
}
func (l *documentLease) LastResult() contract.KVResult {
	l.receiptMu.Lock()
	last := l.last
	l.receiptMu.Unlock()
	if last == nil {
		return contract.KVResult{}
	}
	return last.Clone()
}
func (l *documentLease) recoveryOwned() contract.KVRecovery {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	r := contract.KVRecovery{}
	if l.last != nil {
		r.Last = l.last.Clone()
	}
	if l.unknown != nil {
		v := l.unknown.Clone()
		r.Unknown = &v
	}
	return r
}
func (l *documentLease) retire() error {
	l.mu.Lock()
	l.retired = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	defer l.ops.Unlock()
	l.closePhysical()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	return nil // parked evidence remains owned until original Close/recovery
}
func (l *documentLease) Close() error {
	l.mu.Lock()
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	defer l.ops.Unlock()
	l.closePhysical()
	l.receiptMu.Lock()
	l.last, l.unknown = nil, nil
	l.receiptMu.Unlock()
	receiptBudgetMu.Lock()
	l.s.retained -= l.cacheBytes
	l.cacheBytes = 0
	receiptBudgetMu.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	return nil
}

var _ contract.KVRequestLease = (*documentLease)(nil)
