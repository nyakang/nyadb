package etcd

import (
	"bytes"
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const (
	renewalDocumentLimit = 4
	renewalDocumentBytes = 8 << 20
)

func (s *Session) ReserveKVRenewal(document uint64) (contract.KVRenewalLease, error) {
	return s.reserveRenewal(document, nil, 0)
}

func (s *Session) reserveRenewal(document uint64, previous *renewalLease, inherited int) (contract.KVRenewalLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.client.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	replaced := 0
	credit := 0
	if previous != nil && previous.base.s == s && s.renewals[previous] {
		replaced, credit = 1, previous.charged
	}
	if len(s.renewals)-replaced >= renewalDocumentLimit || s.documentCount()-replaced >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.renewals {
		if l != previous && l.base.document == document {
			return nil, ErrInvalid
		}
	}
	if inherited > renewalDocumentBytes || inherited > retainedSessionBytes-s.retainedBytes+credit {
		return nil, ErrBounds
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &renewalLease{base: &kvLease{s: s, document: document, ctx: ctx, cancel: cancel}, charged: inherited}
	if s.renewals == nil {
		s.renewals = map[*renewalLease]bool{}
	}
	s.renewals[l] = true
	s.retainedBytes += inherited
	if replaced != 0 {
		s.retainedBytes -= previous.charged
		previous.charged = 0
		delete(s.renewals, previous)
	}
	return l, nil
}

func (s *Session) RecoverKVRenewal(previous contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
	old, ok := previous.(*renewalLease)
	if !ok || old == nil {
		return contract.KVRenewalRecovery{}, contract.ErrKVPlanUsed
	}
	before, after := old.Scope(), s.KVScope()
	if before.Namespace != after.Namespace || !bytes.Equal(before.Prefix, after.Prefix) || before.Header.ClusterID != after.Header.ClusterID || before.Header.MemberID != after.Header.MemberID || before.Version != after.Version || !s.Alive() {
		return contract.KVRenewalRecovery{}, contract.ErrRevoked
	}
	result, err := old.settle(true)
	if err != nil {
		return result, err
	}
	old.base.ops.Lock()
	defer old.base.ops.Unlock()
	old.base.s.mu.Lock()
	charge := old.charged
	old.base.s.mu.Unlock()
	fresh, err := s.reserveRenewal(old.base.document, old, charge)
	if err != nil {
		return result, err
	}
	if old.base.s != s {
		old.releaseBudget()
		old.base.s.mu.Lock()
		delete(old.base.s.renewals, old)
		old.base.s.mu.Unlock()
	}
	// Recovery cannot reclassify a lost acknowledgement or authorize another
	// write in the original document, even when every receipt was already Poll'd.
	fresh.(*renewalLease).uncertain = result.Batch.Terminal.WriteOutcomeUnknown
	fresh.(*renewalLease).last = result.Batch
	fresh.(*renewalLease).last.Receipts = nil
	result.Lease = fresh
	return result, nil
}

var _ contract.KVRenewalProvider = (*Session)(nil)
