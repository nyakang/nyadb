package couchdb

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session captures one profile, node observation and authenticated principal.
// CouchDB exposes no atomic database-incarnation guard for a later REST write.
type Session struct {
	mu              sync.Mutex
	profile         connections.Profile
	secret          string
	identity        Identity
	principal       Principal
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	closeDone       chan struct{}
	work            sync.WaitGroup
	leases          map[*queryLease]bool
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.CouchDB || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.CouchDBProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	identity, principal := c.Identity(), c.Principal()
	c.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, secret: secret, identity: identity, principal: principal, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, closeDone: make(chan struct{})}, nil
}
func (s *Session) ProfileID() int64 { return s.profile.ID }

// Published generations are assigned exclusively by dbruntime.
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Objects: true}
}
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: database}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || !validDatabase(n.Catalog) || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
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
func samePrincipal(a, b Principal) bool {
	if (a.Name == nil) != (b.Name == nil) || a.Name != nil && *a.Name != *b.Name {
		return false
	}
	ar, br := slices.Clone(a.Roles), slices.Clone(b.Roles)
	slices.Sort(ar)
	slices.Sort(br)
	return slices.Equal(ar, br)
}
func (s *Session) observeError(err error) error {
	var httpError *HTTPError
	if errors.Is(err, ErrProtocol) || errors.As(err, &httpError) && (httpError.Status == 401 || httpError.Status == 403) {
		s.revoke()
		return errors.Join(contract.ErrRevoked, err)
	}
	return err
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	p, secret, dead := s.profile, s.secret, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	cfg, err := connections.CouchDBProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, s.observeError(err)
	}
	if c.Identity() != s.identity || !samePrincipal(c.Principal(), s.principal) {
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
	identity, principal, err := c.observe(ctx)
	if err != nil {
		return s.observeError(err)
	}
	if identity != s.identity || !samePrincipal(principal, s.principal) {
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
	s.mu.Unlock()
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
