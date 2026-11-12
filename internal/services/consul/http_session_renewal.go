package consul

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpSessionRenewalMaxBytes int64 = 256 << 20
const httpSessionRenewalMaxRenewals = 256

type httpSessionRenewalPreparation struct {
	owner   *httpSessionRenewalLease
	request contract.KVHTTPSessionRenewalRequest
	native  Request
	used    atomic.Bool
}

func (p *httpSessionRenewalPreparation) Snapshot() contract.KVHTTPSessionRenewalRequest {
	return p.request
}

// The base owns the captured client, cancellation, executor lock and receipt
// accounting. A renewal lease retains its own unknown-write barrier; the host
// must also apply it to the other write tools in that same document.
type httpSessionRenewalLease struct {
	base *documentLease
	mu   sync.Mutex
	run  *httpSessionRenewalRun
}

func (l *httpSessionRenewalLease) Scope() contract.KVScope { return l.base.Scope() }
func (l *httpSessionRenewalLease) CheckKVLease() error     { return l.base.CheckKVLease() }
func (l *httpSessionRenewalLease) Limits() contract.KVHTTPSessionRenewalLimits {
	return contract.KVHTTPSessionRenewalLimits{MinInterval: 100 * time.Millisecond, MaxInterval: time.Hour,
		ConnectTimeout: l.base.s.cfg.ConnectTimeout, RequestTimeout: l.base.s.cfg.RequestTimeout,
		MaxRenewals: httpSessionRenewalMaxRenewals, MaxBytes: httpSessionRenewalMaxBytes, TerminalBytes: pendingReceiptBudget}
}

func (l *httpSessionRenewalLease) Prepare(r contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	lim := l.Limits()
	if !validID(r.SessionID) || r.Interval < lim.MinInterval || r.Interval > lim.MaxInterval ||
		r.MaxRenewals < 1 || r.MaxRenewals > lim.MaxRenewals || r.MaxBytes < 1 || r.MaxBytes > lim.MaxBytes {
		return nil, ErrInvalid
	}
	if l.base.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	native := Request{Method: "PUT", Path: "/v1/session/renew/" + r.SessionID}

	return &httpSessionRenewalPreparation{owner: l, request: r, native: native}, nil
}

func (l *httpSessionRenewalLease) Start(parent context.Context, prepared contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
	p, ok := prepared.(*httpSessionRenewalPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	b := l.base
	if !b.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	// On success ownership of ops and the original session work token passes
	// to one worker, covering opening, every write, and final ACK publication.
	success := false
	defer func() {
		if !success {
			b.ops.Unlock()
		}
	}()
	ctx, done, err := b.request(parent)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			done()
		}
	}()
	l.mu.Lock()
	defer l.mu.Unlock()
	if b.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	if l.run != nil {
		return nil, contract.ErrKVBusy // old evidence requires explicit release after known outcomes
	}
	if p.used.Load() {
		return nil, contract.ErrKVPlanUsed
	}
	reserved := p.request.MaxBytes + pendingReceiptBudget
	receiptBudgetMu.Lock()
	if b.cacheBytes+reserved > documentReceiptBudget || b.s.retained+b.s.reserved+reserved > b.s.budget {
		receiptBudgetMu.Unlock()
		return nil, ErrBounds
	}
	b.s.reserved += reserved
	receiptBudgetMu.Unlock()
	if !p.used.CompareAndSwap(false, true) {
		receiptBudgetMu.Lock()
		b.s.reserved -= reserved
		receiptBudgetMu.Unlock()
		return nil, contract.ErrKVPlanUsed
	}
	runCtx, cancel := context.WithCancel(ctx)
	w := &httpSessionRenewalRun{lease: l, request: p.request, cancel: cancel, done: make(chan struct{}),
		pending: make(chan contract.KVHTTPSessionRenewalObservation, p.request.MaxRenewals+1)}
	l.run = w
	success = true
	go w.execute(runCtx, done, p.native.Clone(), reserved)
	return w, nil
}

func (l *httpSessionRenewalLease) LastRenewal() *contract.KVHTTPSessionRenewalSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil {
		return nil
	}
	s := l.run.Snapshot()
	return &s
}

func (l *httpSessionRenewalLease) RetainedRenewal() contract.KVHTTPSessionRenewalRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil {
		return nil
	}
	return l.run
}

// Caller holds base.ops, or is closing a run whose own done has acknowledged.
// mu keeps archive ownership atomic with Start, recovery and late run Close.
func (l *httpSessionRenewalLease) clearRun(expected *httpSessionRenewalRun) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil || expected != nil && l.run != expected {
		return
	}
	l.run.mu.Lock()
	l.run.batch.Observations = nil
	l.run.batch.RetainedBytes = 0
	for len(l.run.pending) > 0 {
		<-l.run.pending
	}
	l.run.mu.Unlock()
	l.run = nil
	receiptBudgetMu.Lock()
	l.base.s.retained -= l.base.cacheBytes
	l.base.cacheBytes = 0
	receiptBudgetMu.Unlock()
}

func (l *httpSessionRenewalLease) ClearReceipts() error {
	if !l.base.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.base.ops.Unlock()
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	if l.base.writeUnknown.Load() {
		return contract.ErrKVWriteUnknown
	}
	l.clearRun(nil)
	return nil
}

func (l *httpSessionRenewalLease) retire() error {
	// retire waits on ops, which the worker releases only after original ACK.
	err := l.base.retire()
	l.base.s.mu.Lock()
	delete(l.base.s.renewalLeases, l)
	l.base.s.mu.Unlock()
	return err
}

func (l *httpSessionRenewalLease) Close() error {
	l.base.mu.Lock()
	l.base.closed = true
	l.base.cancel()
	l.base.mu.Unlock()
	l.base.ops.Lock()
	defer l.base.ops.Unlock()
	l.base.closePhysical()
	l.clearRun(nil)
	l.base.s.mu.Lock()
	delete(l.base.s.renewalLeases, l)
	l.base.s.mu.Unlock()
	return nil
}

type httpSessionRenewalRun struct {
	lease   *httpSessionRenewalLease
	request contract.KVHTTPSessionRenewalRequest
	mu      sync.Mutex
	batch   contract.KVHTTPSessionRenewalBatch
	pending chan contract.KVHTTPSessionRenewalObservation
	cancel  context.CancelFunc
	done    chan struct{}
}

func (w *httpSessionRenewalRun) Request() contract.KVHTTPSessionRenewalRequest { return w.request }
func (w *httpSessionRenewalRun) Snapshot() contract.KVHTTPSessionRenewalSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.batch.Clone()
	select {
	case <-w.done:
	default:
		b.Ended = false
	}
	return contract.KVHTTPSessionRenewalSnapshot{Request: w.request, KVHTTPSessionRenewalBatch: b}
}
func (w *httpSessionRenewalRun) Poll() contract.KVHTTPSessionRenewalBatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.batch
	select {
	case <-w.done:
	default:
		b.Ended = false
	}
	b.Observations = nil
	for {
		select {
		case o := <-w.pending:
			b.Observations = append(b.Observations, o.Clone())
		default:
			return b
		}
	}
}

func (w *httpSessionRenewalRun) PollStatus() contract.KVHTTPSessionRenewalStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.pending) > 0 {
		<-w.pending
	}
	b := w.batch
	acknowledged := false
	select {
	case <-w.done:
		acknowledged = true
	default:
	}
	return contract.KVHTTPSessionRenewalStatus{Observations: len(b.Observations), Ended: b.Ended && acknowledged,
		TerminalOverflow: b.TerminalOverflow, RetainedBytes: b.RetainedBytes, Terminal: b.Terminal}
}

func (w *httpSessionRenewalRun) Observation(ordinal int) (contract.KVHTTPSessionRenewalObservation, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ordinal < 0 || ordinal >= len(w.batch.Observations) {
		return contract.KVHTTPSessionRenewalObservation{}, false
	}
	return w.batch.Observations[ordinal].Clone(), true
}
func (w *httpSessionRenewalRun) Stop(ctx context.Context) error {
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *httpSessionRenewalRun) Close() error {
	w.cancel()
	<-w.done
	// An old run must never wait on a newer run's executor lock. Its done has
	// already acknowledged its own worker; clearRun validates original owner.
	if w.lease.base.writeUnknown.Load() {
		return contract.ErrKVWriteUnknown
	}
	w.lease.clearRun(w)
	return nil
}
