package qdrant

import (
	"context"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session retains the original profile, endpoint and key. Binary observations
// do not identify a cluster, server or collection incarnation.
type Session struct {
	mu              sync.Mutex
	profileID       int64
	config          connections.QdrantConfig
	secret          string
	identity        Identity
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	closeDone       chan struct{}
	work            sync.WaitGroup
	leases          map[*queryLease]bool
	documents       map[*dataLease]bool
	dataSlots       chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Qdrant || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.QdrantProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	identity := c.Identity()
	c.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profileID: p.ID, config: cfg, secret: secret, identity: identity, ctx: root, cancel: cancel, closeDone: make(chan struct{}), leases: map[*queryLease]bool{}}, nil
}

func (s *Session) ProfileID() int64 { return s.profileID }

// Public generations are assigned only by dbruntime.
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	c := nativeDataCapabilities()
	c.Query = true
	c.Objects = true
	return c
}
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Namespace(collection string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: collection}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if _, err := collectionPath(n.Catalog); err != nil || n.ProfileID != s.ProfileID() || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}

// Admission and Close share the lock; Wait cannot race with a new worker.
func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, nil, contract.ErrRevoked
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	cfg, secret, dead := s.config, s.secret, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	if c.Identity() != s.identity {
		c.Close()
		s.revoke()
		return nil, contract.ErrRevoked
	}
	if !s.Alive() || ctx.Err() != nil {
		c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrRevoked
	}
	return c, nil
}
func (s *Session) validateObservation(ctx context.Context, c *Client) error {
	identity, err := c.observe(ctx)
	if err != nil {
		return err
	}
	if identity != s.identity {
		s.revoke()
		return contract.ErrRevoked
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return ctx.Err()
}
func (s *Session) revoke() {
	s.mu.Lock()
	s.revoked, s.secret = true, ""
	s.cancel()
	s.mu.Unlock()
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return nil
	}
	s.closed, s.secret = true, ""
	s.cancel()
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	documents := make([]*dataLease, 0, len(s.documents))
	for d := range s.documents {
		documents = append(documents, d)
	}
	s.mu.Unlock()
	for _, d := range documents {
		d.Close()
	}
	for _, l := range leases {
		l.Release()
	}
	s.work.Wait()
	close(s.closeDone)
	return nil
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
