package neo4j

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session owns the original metadata client and independent original Cypher
// clients. Agent/protocol/address are observations, not database incarnation IDs.
type Session struct {
	mu                      sync.Mutex
	profileID               int64
	database                string
	config                  connections.Neo4jConfig
	secret                  string
	identity                Identity
	client                  *Client
	openClient              func(context.Context, connections.Neo4jConfig, string) (*Client, error)
	ctx                     context.Context
	cancel                  context.CancelFunc
	closed, revoked         bool
	closeDone               chan struct{}
	closeErr                error
	work                    sync.WaitGroup
	leases                  map[*queryLease]bool
	dataLeases              map[*dataLease]bool
	schemaLeases            map[*schemaPreparationLease]bool
	dataSlots               chan struct{}
	metadata, metadataQueue chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, e := connections.NormalizeProfile(p)
	if e != nil {
		return nil, e
	}
	if p.Kind != connections.Neo4j || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, e := connections.Neo4jProfileConfig(p)
	if e != nil {
		return nil, e
	}
	c, e := Open(ctx, cfg, secret)
	if e != nil {
		return nil, e
	}
	// A configured/home database is resolved by the original native operation,
	// never guessed from username, a navigation label or an assumed default.
	pplan, e := c.Prepare(Request{Query: databaseProbe(cfg.Database), Database: cfg.Database, Mode: "read", MaxRows: 2})
	var r Response
	if e == nil {
		r, e = c.Execute(ctx, pplan)
	}
	if e == nil && (!r.Completed || r.Truncated || len(r.Columns) != 1 || r.Columns[0] != "name" || len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].Kind != "string" || r.Rows[0][0].Text != r.Summary.Database || !validDatabase(r.Summary.Database) || cfg.Database != "" && cfg.Database != r.Summary.Database) {
		e = ErrProtocol
	}
	if e == nil {
		e = ctx.Err()
	}
	if e != nil {
		return nil, errors.Join(e, c.Close())
	}
	cfg.Database = r.Summary.Database
	s := newSession(p.ID, cfg, secret, c)
	return s, nil
}
func newSession(profileID int64, cfg connections.Neo4jConfig, secret string, c *Client) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{profileID: profileID, database: cfg.Database, config: cfg, secret: secret, identity: c.Identity(), client: c, openClient: Open, ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), leases: map[*queryLease]bool{}, schemaLeases: map[*schemaPreparationLease]bool{}, metadata: make(chan struct{}, 1), metadataQueue: make(chan struct{}, 4)}
}
func (s *Session) ProfileID() int64 { return s.profileID }

// Public generations are published only at the runtime boundary.
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	c := s.DataDocumentCapabilities()
	c.Query, c.Objects, c.Documents = true, true, true
	return c
}
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if database == "" {
		database = s.database
	}
	return contract.NamespaceRef{ProfileID: s.profileID, Catalog: database}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.profileID || n.Schema != "" || !validDatabase(n.Catalog) {
		return ErrInvalid
	}
	return nil
}
func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, nil, contract.ErrRevoked
	}
	if e := ctx.Err(); e != nil {
		return nil, nil, e
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) revoke() { s.mu.Lock(); s.revoked = true; s.secret = ""; s.cancel(); s.mu.Unlock() }
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	cfg, secret, dead, opener := s.config, s.secret, s.closed || s.revoked, s.openClient
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	c, e := opener(ctx, cfg, secret)
	if e != nil {
		return nil, e
	}
	i := c.Identity()
	if i.Agent != s.identity.Agent || i.ProtocolMajor != s.identity.ProtocolMajor || i.ProtocolMinor != s.identity.ProtocolMinor {
		_ = c.Close()
		s.revoke()
		return nil, contract.ErrRevoked
	}
	if !s.Alive() || ctx.Err() != nil {
		_ = c.Close()
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		return nil, contract.ErrRevoked
	}
	return c, nil
}
func (s *Session) withMetadata(ctx context.Context, read func(context.Context, *Client) error) error {
	ctx, done, e := s.request(ctx)
	if e != nil {
		return e
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
	e = read(ctx, s.client)
	if !s.client.Alive() || errors.Is(e, ErrProtocol) {
		s.revoke()
		e = errors.Join(e, s.client.Close())
		return errors.Join(contract.ErrRevoked, e)
	}
	if e != nil {
		return e
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return ctx.Err()
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
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	datas := make([]*dataLease, 0, len(s.dataLeases))
	for l := range s.dataLeases {
		datas = append(datas, l)
	}
	schemas := make([]*schemaPreparationLease, 0, len(s.schemaLeases))
	for l := range s.schemaLeases {
		schemas = append(schemas, l)
	}
	s.mu.Unlock()
	closeErr := s.client.Close()
	for _, l := range leases {
		l.Release()
	}
	for _, l := range datas {
		closeErr = errors.Join(closeErr, l.Close())
	}
	for _, l := range schemas {
		closeErr = errors.Join(closeErr, l.Close())
	}
	s.work.Wait()
	s.closeErr = closeErr
	close(s.closeDone)
	return closeErr
}

var _ contract.Session = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
