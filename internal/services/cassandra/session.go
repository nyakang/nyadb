package cassandra

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var ErrMetadataChanged = errors.New("Cassandra metadata changed during the original read; refresh explicitly")

// Session owns a single original metadata connection and independent physical
// query leases. Native host_id is a node observation, not a cluster UUID.
type Session struct {
	mu              sync.Mutex
	profile         connections.Profile
	secret          string
	identity        Identity
	client          *Client
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	closeDone       chan struct{}
	work            sync.WaitGroup
	leases          map[*queryLease]bool
	datas           map[*dataLease]bool
	dataSlots       chan struct{}
	metadata        chan struct{}
	metadataQueue   chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Cassandra || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.CassandraProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		c.Close()
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, secret: secret, identity: c.Identity(), client: c, ctx: root, cancel: cancel, closeDone: make(chan struct{}), leases: map[*queryLease]bool{}, datas: map[*dataLease]bool{}, dataSlots: make(chan struct{}, 4), metadata: make(chan struct{}, 1), metadataQueue: make(chan struct{}, 4)}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	v := s.DataDocumentCapabilities()
	v.Query, v.Objects = true, true
	return v
}
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Namespace(label string) contract.NamespaceRef {
	if label == "" {
		label = s.profile.Database
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: label}
}
func validKeyspace(name string) bool {
	cfg, err := connections.NormalizeCassandraConfig(connections.CassandraConfig{Endpoint: "localhost:9042", Keyspace: name})
	return err == nil && cfg.Keyspace != ""
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Schema != "" || !validKeyspace(n.Catalog) {
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
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) revoke()        { s.mu.Lock(); s.revoked = true; s.secret = ""; s.cancel(); s.mu.Unlock() }
func sameNode(a, b Identity) bool { a.SchemaVersion = ""; b.SchemaVersion = ""; return a == b }
func (s *Session) validateNode(ctx context.Context, c *Client) (Identity, error) {
	r, err := c.execute(ctx, Request{Statement: identityQuery, PageSize: 2}, true)
	if err != nil {
		if !c.Alive() && c == s.client {
			s.revoke()
			return Identity{}, errors.Join(contract.ErrRevoked, err)
		}
		return Identity{}, err
	}
	i, err := readIdentity(r)
	if err != nil || !sameNode(i, s.identity) {
		s.revoke()
		return Identity{}, contract.ErrRevoked
	}
	if !s.Alive() {
		return Identity{}, contract.ErrRevoked
	}
	return i, ctx.Err()
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	p, secret, dead := s.profile, s.secret, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	cfg, err := connections.CassandraProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	if !sameNode(c.Identity(), s.identity) || !s.Alive() {
		c.Close()
		s.revoke()
		return nil, contract.ErrRevoked
	}
	return c, nil
}
func (s *Session) withMetadata(ctx context.Context, read func(context.Context, *Client) error) error {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	select {
	case s.metadataQueue <- struct{}{}:
		defer func() { <-s.metadataQueue }()
	default:
		return ErrBusy
	}
	select {
	case s.metadata <- struct{}{}:
		defer func() { <-s.metadata }()
	case <-ctx.Done():
		return ctx.Err()
	}
	before, err := s.validateNode(ctx, s.client)
	if err != nil {
		return err
	}
	if err = read(ctx, s.client); err != nil {
		if !s.client.Alive() {
			s.revoke()
			return errors.Join(contract.ErrRevoked, err)
		}
		return err
	}
	after, err := s.validateNode(ctx, s.client)
	if err != nil {
		return err
	}
	if before.SchemaVersion != after.SchemaVersion {
		return ErrMetadataChanged
	}
	return ctx.Err()
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return nil
	}
	s.closed = true
	s.secret = ""
	s.cancel()
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	datas := make([]*dataLease, 0, len(s.datas))
	for l := range s.datas {
		datas = append(datas, l)
	}
	s.mu.Unlock()
	s.client.Close()
	for _, l := range leases {
		l.Release()
	}
	for _, l := range datas {
		l.Close()
	}
	s.work.Wait()
	close(s.closeDone)
	return nil
}

var _ contract.Session = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
