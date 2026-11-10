package etcd

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session retains its original handshake channel and independent document
// channels. Cluster/member IDs are observations, not incarnation guarantees.
type Session struct {
	mu              sync.Mutex
	profile         connections.Profile
	secret          string
	identity        Identity
	prefix          []byte
	client          *Client
	opener          func(context.Context, connections.Profile, string) (*Client, error)
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	work            sync.WaitGroup
	leases          map[*kvLease]bool
	dataDocuments   map[*dataLease]bool
	events          map[*eventsLease]bool
	renewals        map[*renewalLease]bool
	retainedBytes   int
	closeDone       chan struct{}
	closeErr        error
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p.Variants = append([]connections.ConnectionVariant(nil), p.Variants...)
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Etcd || p.ID <= 0 {
		return nil, ErrInvalid
	}
	c, err := OpenProfile(ctx, p, secret)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return newSession(p, secret, c), nil
}
func newSession(p connections.Profile, secret string, c *Client) *Session {
	p.Variants = append([]connections.ConnectionVariant(nil), p.Variants...)
	root, cancel := context.WithCancel(context.Background())
	prefix, _ := connections.EtcdPrefixBytes(p.EtcdOptions)
	return &Session{profile: p, secret: secret, identity: c.Identity(), prefix: prefix, client: c, opener: OpenProfile, ctx: root, cancel: cancel, leases: map[*kvLease]bool{}, closeDone: make(chan struct{})}
}
func (s *Session) ProfileID() int64                    { return s.profile.ID }
func (s *Session) Generation() uint64                  { return 1 } // publication belongs to dbruntime
func (s *Session) Capabilities() contract.Capabilities { return dataCapabilities() }
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.revoked && s.client.Alive()
}
func (s *Session) KVScope() contract.KVScope {
	return contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: s.profile.ID, Catalog: "kv"}, Generation: 1, Prefix: bytes.Clone(s.prefix), Header: contract.KVHeader{ClusterID: s.identity.ClusterID, MemberID: s.identity.MemberID, Revision: s.identity.Revision, RaftTerm: s.identity.RaftTerm}, Version: s.identity.Version}
}
func (s *Session) request(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.client.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	s.work.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	p, secret, opener, dead := s.profile, s.secret, s.opener, s.closed || s.revoked
	p.Variants = append([]connections.ConnectionVariant(nil), p.Variants...)
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	c, err := opener(ctx, p, secret)
	if err != nil {
		return nil, err
	}
	i := c.Identity()
	if i.ClusterID != s.identity.ClusterID || i.MemberID != s.identity.MemberID || i.Version != s.identity.Version {
		_ = c.Close()
		s.mu.Lock()
		s.revoked = true
		s.secret = ""
		s.cancel()
		s.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if !s.Alive() || ctx.Err() != nil {
		_ = c.Close()
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		return nil, contract.ErrRevoked
	}
	return c, nil
}

// documentCount is called with s.mu held. All document families share one cap.
func (s *Session) documentCount() int {
	return len(s.leases) + len(s.dataDocuments) + len(s.events) + len(s.renewals)
}
func (s *Session) ReserveKV(document uint64) (contract.KVRequestLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.client.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if s.documentCount() >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.leases {
		if l.document == document {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &kvLease{s: s, document: document, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func (s *Session) RecoverKV(previous contract.KVRequestLease) (contract.KVRecovery, error) {
	old, ok := previous.(*kvLease)
	if !ok || old == nil {
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	before, after := old.Scope(), s.KVScope()
	if before.Namespace != after.Namespace || !bytes.Equal(before.Prefix, after.Prefix) || before.Header.ClusterID != after.Header.ClusterID || before.Header.MemberID != after.Header.MemberID || before.Version != after.Version {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	if !s.Alive() {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	last, err := old.settle()
	if err != nil {
		return contract.KVRecovery{Last: last}, err
	}
	fresh, err := s.ReserveKV(old.document)
	return contract.KVRecovery{Lease: fresh, Last: last}, err
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	s.secret = ""
	s.cancel()
	leases := make([]*kvLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	documents := make([]*dataLease, 0, len(s.dataDocuments))
	for l := range s.dataDocuments {
		documents = append(documents, l)
	}
	events := make([]*eventsLease, 0, len(s.events))
	for l := range s.events {
		events = append(events, l)
	}
	renewals := make([]*renewalLease, 0, len(s.renewals))
	for l := range s.renewals {
		renewals = append(renewals, l)
	}
	s.mu.Unlock()
	err := s.client.Close()
	for _, l := range leases {
		err = errors.Join(err, l.Close())
	}
	for _, l := range documents {
		err = errors.Join(err, l.Close())
	}
	for _, l := range events {
		err = errors.Join(err, l.retire())
	}
	for _, l := range renewals {
		err = errors.Join(err, l.retire())
	}
	s.work.Wait()
	s.closeErr = err
	close(s.closeDone)
	return err
}

var _ contract.KVRequestsProvider = (*Session)(nil)
