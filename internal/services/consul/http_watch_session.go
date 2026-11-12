package consul

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) ReserveKVHTTPWatch(document uint64) (contract.KVHTTPWatchLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.root.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.leases)+len(s.dataLeases)+len(s.watchLeases) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.watchLeases {
		if l.base.document == document {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &httpWatchLease{base: &documentLease{s: s, document: document, ctx: ctx, cancel: cancel}}
	if s.watchLeases == nil {
		s.watchLeases = map[*httpWatchLease]bool{}
	}
	s.watchLeases[l] = true
	return l, nil
}

func (s *Session) RecoverKVHTTPWatch(previous contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
	old, ok := previous.(*httpWatchLease)
	if !ok || old == nil {
		return contract.KVHTTPWatchRecovery{}, contract.ErrKVPlanUsed
	}
	if !compatibleScopes(old.Scope(), s.KVScope()) || !s.Alive() {
		return contract.KVHTTPWatchRecovery{}, contract.ErrRevoked
	}
	b := old.base
	b.mu.Lock()
	if b.closed || b.recovered {
		b.mu.Unlock()
		return contract.KVHTTPWatchRecovery{}, contract.ErrKVPlanUsed
	}
	b.retired = true
	b.cancel()
	b.mu.Unlock()
	b.ops.Lock()
	defer b.ops.Unlock()
	b.mu.Lock()
	invalid := b.closed || b.recovered
	b.mu.Unlock()
	if invalid {
		return contract.KVHTTPWatchRecovery{}, contract.ErrKVPlanUsed
	}
	b.closePhysical()
	result := contract.KVHTTPWatchRecovery{Watch: old.LastWatch()}
	b.s.mu.Lock()
	delete(b.s.watchLeases, old)
	b.s.mu.Unlock()
	fresh, err := s.ReserveKVHTTPWatch(b.document)
	if err != nil {
		return result, err
	}
	next := fresh.(*httpWatchLease)
	if err = transferWatchEvidence(old, next); err != nil {
		next.Close()
		return result, err
	}
	b.mu.Lock()
	b.recovered = true
	b.mu.Unlock()
	result.Lease = next
	return result, nil
}

// The original worker has acknowledged (old.base.ops is held). Transfer only
// evidence and accounting, never its client, request preparation or execution.
func transferWatchEvidence(old, next *httpWatchLease) error {
	next.base.ops.Lock()
	defer next.base.ops.Unlock()
	if err := next.CheckKVLease(); err != nil {
		return err
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	next.mu.Lock()
	defer next.mu.Unlock()
	// A late original run Close can release its settled archive independently
	// of ops. Transfer archive and accounting together under that same owner.
	receiptBudgetMu.Lock()
	credit := int64(0)
	if old.base.s == next.base.s {
		credit = old.base.cacheBytes
	}
	if next.base.s.retained+next.base.s.reserved+old.base.cacheBytes-credit > next.base.s.budget {
		receiptBudgetMu.Unlock()
		return ErrBounds
	}
	old.base.s.retained -= old.base.cacheBytes
	next.base.s.retained += old.base.cacheBytes
	next.base.cacheBytes = old.base.cacheBytes
	old.base.cacheBytes = 0
	receiptBudgetMu.Unlock()
	if w := old.run; w != nil {
		w.mu.Lock()
		retained := &httpWatchRun{lease: next, request: w.request, batch: w.batch,
			cancel: func() {}, done: make(chan struct{}), pending: make(chan contract.KVHTTPWatchObservation, w.request.MaxObservations+1)}
		close(retained.done)
		for _, o := range retained.batch.Observations {
			retained.pending <- o
		}
		w.batch.Observations, w.batch.RetainedBytes = nil, 0
		for len(w.pending) > 0 {
			<-w.pending
		}
		w.mu.Unlock()
		next.run = retained
		old.run = nil
	}
	return nil
}

var _ contract.KVHTTPWatchProvider = (*Session)(nil)
