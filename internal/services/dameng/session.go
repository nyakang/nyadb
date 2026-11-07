package dameng

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session keeps background metadata and document physical connections in
// separate bounded pools. Public generations belong to dbruntime, not DPI.
type Session struct {
	mu        sync.Mutex
	profile   connections.Profile
	db, sqlDB *sql.DB
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	closeDone chan struct{}
	closeErr  error
	requests  sync.WaitGroup
	leases    map[*queryLease]struct{}
	slots     chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Dameng || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.DamengProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := NewSessionConnector(cfg, password)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(cfg.Options.MaxConnections)
	db.SetMaxIdleConns(min(2, cfg.Options.MaxConnections))
	openCtx, stop := context.WithTimeout(ctx, time.Duration(cfg.Options.ConnectTimeoutSeconds)*time.Second)
	defer stop()
	if err = db.PingContext(openCtx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	sqlDB := sql.OpenDB(c)
	sqlDB.SetMaxOpenConns(cfg.Options.SQLMaxConnections)
	sqlDB.SetMaxIdleConns(0)
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, db: db, sqlDB: sqlDB, ctx: sctx, cancel: cancel, closeDone: make(chan struct{}), leases: make(map[*queryLease]struct{}), slots: make(chan struct{}, cfg.Options.SQLMaxConnections)}, nil
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.profile.Database }
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Schemas: true}
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	s.requests.Add(1)
	s.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.requests.Done() }, nil
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	s.cancel()
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	s.requests.Wait()
	s.closeErr = errors.Join(s.sqlDB.Close(), s.db.Close())
	close(s.closeDone)
	return s.closeErr
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
