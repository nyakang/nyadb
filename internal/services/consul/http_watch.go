package consul

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpWatchMaxBytes int64 = 256 << 20
const httpWatchMaxObservations = 256

type httpWatchPreparation struct {
	owner   *httpWatchLease
	request contract.KVHTTPWatchRequest
	native  Request
	used    atomic.Bool
}

func (p *httpWatchPreparation) Snapshot() contract.KVHTTPWatchRequest { return p.request }

// The base owns the captured client, cancellation, executor lock and receipt
// accounting. Watch uses a separate registration and never shares write state.
type httpWatchLease struct {
	base *documentLease
	mu   sync.Mutex
	run  *httpWatchRun
}

func (l *httpWatchLease) Scope() contract.KVScope { return l.base.Scope() }
func (l *httpWatchLease) CheckKVLease() error     { return l.base.CheckKVLease() }
func (l *httpWatchLease) Limits() contract.KVHTTPWatchLimits {
	return contract.KVHTTPWatchLimits{MinWait: 100 * time.Millisecond, MaxWait: 10 * time.Minute,
		ConnectTimeout: l.base.s.cfg.ConnectTimeout, RequestTimeout: l.base.s.cfg.RequestTimeout,
		MaxObservations: httpWatchMaxObservations, MaxBytes: httpWatchMaxBytes, TerminalBytes: pendingReceiptBudget}
}

func (l *httpWatchLease) Prepare(r contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	lim := l.Limits()
	if !validKey(r.Target, r.Prefix) || !validKey(r.ObservationPrefix, true) || !strings.HasPrefix(r.Target, r.ObservationPrefix) ||
		r.Wait < lim.MinWait || r.Wait > lim.MaxWait || r.Wait+r.Wait/16+lim.ConnectTimeout >= lim.RequestTimeout ||
		r.MaxObservations < 1 || r.MaxObservations > lim.MaxObservations || r.MaxBytes < 1 || r.MaxBytes > lim.MaxBytes {
		return nil, ErrInvalid
	}
	path, err := KeyPath(r.ObservationPrefix)
	if err != nil {
		return nil, err
	}
	native := Request{Method: "GET", Path: path, Query: url.Values{"recurse": {""}, "index": {strconv.FormatUint(r.StartIndex, 10)}, "wait": {r.Wait.String()}}}
	if _, write, _, err := validateRequest(native); err != nil || write {
		return nil, errors.Join(err, ErrInvalid)
	}
	return &httpWatchPreparation{owner: l, request: r, native: native}, nil
}

func (l *httpWatchLease) Start(parent context.Context, prepared contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
	p, ok := prepared.(*httpWatchPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	b := l.base
	if !b.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	// On success ownership of ops and the original session work token passes
	// to one worker, covering opening, every read, and final ACK publication.
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
	if l.run != nil {
		return nil, contract.ErrKVBusy // old evidence requires explicit release
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
	w := &httpWatchRun{lease: l, request: p.request, cancel: cancel, done: make(chan struct{}),
		pending: make(chan contract.KVHTTPWatchObservation, p.request.MaxObservations+1)}
	l.run = w
	success = true
	go w.execute(runCtx, done, p.native.Clone(), reserved)
	return w, nil
}

func (l *httpWatchLease) LastWatch() *contract.KVHTTPWatchSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil {
		return nil
	}
	s := l.run.Snapshot()
	return &s
}

// Caller holds base.ops, or is closing a run whose own done has acknowledged.
// mu keeps archive ownership atomic with Start, recovery and late run Close.
func (l *httpWatchLease) clearRun(expected *httpWatchRun) {
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

func (l *httpWatchLease) ClearObservations() error {
	if !l.base.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.base.ops.Unlock()
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	l.clearRun(nil)
	return nil
}

func (l *httpWatchLease) retire() error {
	// retire waits on ops, which the worker releases only after original ACK.
	err := l.base.retire()
	l.base.s.mu.Lock()
	delete(l.base.s.watchLeases, l)
	l.base.s.mu.Unlock()
	return err
}

func (l *httpWatchLease) Close() error {
	l.base.mu.Lock()
	l.base.closed = true
	l.base.cancel()
	l.base.mu.Unlock()
	l.base.ops.Lock()
	defer l.base.ops.Unlock()
	l.base.closePhysical()
	l.clearRun(nil)
	l.base.s.mu.Lock()
	delete(l.base.s.watchLeases, l)
	l.base.s.mu.Unlock()
	return nil
}

type httpWatchRun struct {
	lease   *httpWatchLease
	request contract.KVHTTPWatchRequest
	mu      sync.Mutex
	batch   contract.KVHTTPWatchBatch
	pending chan contract.KVHTTPWatchObservation
	cancel  context.CancelFunc
	done    chan struct{}
}

func (w *httpWatchRun) Request() contract.KVHTTPWatchRequest { return w.request }
func (w *httpWatchRun) Snapshot() contract.KVHTTPWatchSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return contract.KVHTTPWatchSnapshot{Request: w.request, KVHTTPWatchBatch: w.batch.Clone()}
}
func (w *httpWatchRun) Poll() contract.KVHTTPWatchBatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.batch
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
func (w *httpWatchRun) Stop(ctx context.Context) error {
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *httpWatchRun) Close() error {
	w.cancel()
	<-w.done
	// An old run must never wait on a newer run's executor lock. Its done has
	// already acknowledged its own worker; clearRun validates original owner.
	w.lease.clearRun(w)
	return nil
}

func watchObservation(r Response, request contract.KVHTTPWatchRequest, index uint64) contract.KVHTTPWatchObservation {
	h := documentResult(contract.KVRequest{}, r).HTTP
	o := contract.KVHTTPWatchObservation{HTTP: *h, ReceivedAt: time.Now().UTC(),
		ProjectionComplete: h.Ended && h.Captured && h.EOF && h.Acknowledged && h.Succeeded && h.ProjectionError == "" && h.CaptureError == ""}
	if o.ProjectionComplete {
		for i, e := range h.Entries {
			if e.Key == request.Target || request.Prefix && strings.HasPrefix(e.Key, request.Target) {
				o.Matches = append(o.Matches, i)
			}
		}
	}
	o.IndexRelation = "unavailable"
	if h.ProjectionError != "" {
		o.IndexRelation = "invalid"
	} else if next := h.Metadata.Index; next != nil {
		switch {
		case *next == 0:
			o.IndexRelation = "zero"
		case *next < index:
			o.IndexRelation = "backward"
		case *next == index:
			o.IndexRelation = "unchanged"
		case index == 0:
			o.IndexRelation = "initial"
		default:
			o.IndexRelation = "advanced"
		}
	}
	return o
}

func (w *httpWatchRun) execute(ctx context.Context, done func(), native Request, reserved int64) {
	b := w.lease.base
	terminal := contract.KVHTTPWatchTerminal{Reason: "stopped"}
	defer func() {
		w.cancel()
		receiptBudgetMu.Lock()
		b.s.reserved -= reserved
		receiptBudgetMu.Unlock()
		w.mu.Lock()
		w.batch.Ended, w.batch.Terminal = true, terminal
		w.mu.Unlock()
		// Keep Stop/Close ACK behind all physical cleanup and accounting.
		done()
		b.ops.Unlock()
		close(w.done)
	}()
	if err := b.connect(ctx); err != nil {
		terminal = contract.KVHTTPWatchTerminal{Reason: "connection-lost", Message: err.Error()}
		return
	}
	index := w.request.StartIndex
	for n := 0; n < w.request.MaxObservations; n++ {
		if ctx.Err() != nil {
			return
		}
		if err := b.CheckKVLease(); err != nil {
			terminal = contract.KVHTTPWatchTerminal{Reason: "lease-lost", Message: err.Error()}
			return
		}
		native.Query.Set("index", strconv.FormatUint(index, 10))
		p, err := b.client.Prepare(native)
		if err != nil {
			terminal = contract.KVHTTPWatchTerminal{Reason: "request-rejected", Message: err.Error()}
			return
		}
		r, err := b.client.Execute(ctx, p)
		o := watchObservation(r, w.request, index)
		cost := resultCost(&contract.KVResult{HTTP: &o.HTTP}) + int64(8*len(o.Matches))
		// Native response/projection bounds guarantee this fits the prepaid
		// terminal slot, including a captured responseLimit+1 prefix on failure.
		if cost > pendingReceiptBudget {
			panic("Consul HTTP Watch receipt reservation invariant")
		}
		receiptBudgetMu.Lock()
		b.s.reserved -= cost
		b.s.retained += cost
		b.cacheBytes += cost
		reserved -= cost
		receiptBudgetMu.Unlock()
		w.mu.Lock()
		w.batch.RetainedBytes += cost
		w.batch.TerminalOverflow = w.batch.RetainedBytes > w.request.MaxBytes
		overflow := w.batch.TerminalOverflow
		w.batch.Observations = append(w.batch.Observations, o)
		w.pending <- o // capacity is reserved for every possible original result
		w.mu.Unlock()
		if !b.client.Alive() {
			err = b.markLost(err)
		}
		switch {
		case err != nil:
			terminal = contract.KVHTTPWatchTerminal{Reason: "read-failed", Message: err.Error()}
			return
		case !o.ProjectionComplete:
			terminal = contract.KVHTTPWatchTerminal{Reason: "projection-incomplete"}
			return
		case overflow:
			terminal = contract.KVHTTPWatchTerminal{Reason: "byte-limit"}
			return
		case o.IndexRelation == "zero" || o.IndexRelation == "backward" || o.IndexRelation == "unavailable" || o.IndexRelation == "invalid":
			terminal = contract.KVHTTPWatchTerminal{Reason: "index-" + o.IndexRelation}
			return
		}
		index = *o.HTTP.Metadata.Index
	}
	terminal = contract.KVHTTPWatchTerminal{Reason: "observation-limit"}
}

var _ contract.KVHTTPWatchLease = (*httpWatchLease)(nil)
var _ contract.KVHTTPWatchRun = (*httpWatchRun)(nil)
