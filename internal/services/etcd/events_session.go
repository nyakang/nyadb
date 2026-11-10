package etcd

import (
	"bytes"
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) ReserveKVEvents(document uint64) (contract.KVEventsLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.client.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.events) >= eventDocumentLimit || len(s.events)+len(s.leases)+len(s.dataDocuments) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.events {
		if l.base.document == document {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &eventsLease{base: &kvLease{s: s, document: document, ctx: ctx, cancel: cancel}}
	if s.events == nil {
		s.events = map[*eventsLease]bool{}
	}
	s.events[l] = true
	return l, nil
}

func (s *Session) RecoverKVEvents(previous contract.KVEventsLease) (contract.KVEventsRecovery, error) {
	old, ok := previous.(*eventsLease)
	if !ok || old == nil {
		return contract.KVEventsRecovery{}, contract.ErrKVPlanUsed
	}
	before, after := old.Scope(), s.KVScope()
	if before.Namespace != after.Namespace || !bytes.Equal(before.Prefix, after.Prefix) || before.Header.ClusterID != after.Header.ClusterID || before.Header.MemberID != after.Header.MemberID || before.Version != after.Version || !s.Alive() {
		return contract.KVEventsRecovery{}, contract.ErrRevoked
	}
	result, err := old.settle()
	if err != nil {
		return result, err
	}
	result.Lease, err = s.ReserveKVEvents(old.base.document)
	return result, err
}

var _ contract.KVEventsProvider = (*Session)(nil)
