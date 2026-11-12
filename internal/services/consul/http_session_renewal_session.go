package consul

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) ReserveKVHTTPSessionRenewal(document uint64) (contract.KVHTTPSessionRenewalLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.root.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.leases)+len(s.dataLeases)+len(s.watchLeases)+len(s.renewalLeases) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.renewalLeases {
		if l.base.document == document {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &httpSessionRenewalLease{base: &documentLease{s: s, document: document, ctx: ctx, cancel: cancel}}
	if s.renewalLeases == nil {
		s.renewalLeases = map[*httpSessionRenewalLease]bool{}
	}
	s.renewalLeases[l] = true
	return l, nil
}

func (s *Session) RecoverKVHTTPSessionRenewal(previous contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
	old, ok := previous.(*httpSessionRenewalLease)
	if !ok || old == nil {
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrKVPlanUsed
	}
	if !compatibleScopes(old.Scope(), s.KVScope()) || !s.Alive() {
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrRevoked
	}
	b := old.base
	b.mu.Lock()
	if b.closed || b.recovered {
		b.mu.Unlock()
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrKVPlanUsed
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
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrKVPlanUsed
	}
	// The worker releases ops immediately before publishing done. Wait for
	// that original notification as well, so recovery never returns an active
	// snapshot of an already transferred, settled archive.
	old.mu.Lock()
	run := old.run
	old.mu.Unlock()
	if run != nil {
		<-run.done
	}
	b.closePhysical()
	result := contract.KVHTTPSessionRenewalRecovery{Renewal: old.LastRenewal()}
	b.s.mu.Lock()
	delete(b.s.renewalLeases, old)
	b.s.mu.Unlock()
	fresh, err := s.ReserveKVHTTPSessionRenewal(b.document)
	if err != nil {
		return result, err
	}
	next := fresh.(*httpSessionRenewalLease)
	if err = transferSessionRenewalEvidence(old, next); err != nil {
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
func transferSessionRenewalEvidence(old, next *httpSessionRenewalLease) error {
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
	next.base.writeUnknown.Store(old.base.writeUnknown.Load())
	old.base.cacheBytes = 0
	receiptBudgetMu.Unlock()
	if w := old.run; w != nil {
		w.mu.Lock()
		retained := &httpSessionRenewalRun{lease: next, request: w.request, batch: w.batch,
			cancel: func() {}, done: make(chan struct{}), pending: make(chan contract.KVHTTPSessionRenewalObservation, w.request.MaxRenewals+1)}
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

var _ contract.KVHTTPSessionRenewalProvider = (*Session)(nil)
