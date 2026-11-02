// Package engines implements PostgreSQL and user SQLite sessions. Configuration
// storage uses a separate modernc handle owned by localdb.
package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var ErrRevoked = contract.ErrRevoked
var ErrConflict = errors.New("row changed or no longer exists")
var ErrUncertain = errors.New("commit outcome uncertain; do not replay")

type Manager struct {
	mu          sync.Mutex
	next        uint64
	sessions    map[int64]*Session
	intents     map[int64]uint64
	DialContext func(context.Context, string, string) (net.Conn, error)
}

func NewManager() *Manager {
	return &Manager{sessions: make(map[int64]*Session), intents: make(map[int64]uint64)}
}
func (m *Manager) Connect(ctx context.Context, p connections.Profile, password string) error {
	if p.Kind != connections.PostgreSQL && p.Kind != connections.SQLite {
		return fmt.Errorf("unsupported engine %s", p.Kind)
	}
	if p.ID <= 0 || p.Database == "" {
		return errors.New("profile and fixed database/file are required")
	}
	m.mu.Lock()
	m.next++
	intent := m.next
	m.intents[p.ID] = intent
	m.mu.Unlock()
	strategy, err := strategyFor(p.Kind)
	if err != nil {
		return err
	}
	if p.Kind == connections.SQLite {
		options, e := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
		if e != nil {
			return e
		}
		p.SQLiteOptions = options
		strategy = sqliteDialect{options: options}
	}
	db, err := strategy.open(p, password, m.DialContext)
	if err != nil {
		return err
	}
	maxConnections := 8
	if p.Kind == connections.PostgreSQL {
		options, e := connections.NormalizePostgreSQLOptions(p.PostgreSQLOptions)
		if e != nil {
			db.Close()
			return e
		}
		maxConnections = options.MaxConnections
	}
	db.SetMaxOpenConns(maxConnections)
	db.SetMaxIdleConns(min(2, maxConnections))
	if e := db.PingContext(ctx); e != nil {
		db.Close()
		return redact(e, password)
	}
	m.mu.Lock()
	if ctx.Err() != nil || m.intents[p.ID] != intent {
		m.mu.Unlock()
		db.Close()
		return ErrRevoked
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	s := &Session{dataScope: rand.Text(), db: db, strategy: strategy, kind: p.Kind, id: p.ID, generation: intent, ctx: sessionCtx, cancel: sessionCancel, catalog: p.Database, leases: make(map[*Lease]bool), transactions: make(map[*Tx]bool)}
	old := m.sessions[p.ID]
	m.sessions[p.ID] = s
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}
func redact(e error, password string) error {
	if e != nil && password != "" {
		return errors.New(strings.ReplaceAll(e.Error(), password, "[hidden]"))
	}
	return e
}
func (m *Manager) Session(id int64) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return nil, ErrRevoked
	}
	return s, nil
}
func (m *Manager) IsConnected(id int64) bool { s, e := m.Session(id); return e == nil && !s.isClosed() }
func (m *Manager) Disconnect(id int64) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.next++
	m.intents[id] = m.next
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	all := m.sessions
	m.next++
	for id := range m.intents {
		m.intents[id] = m.next
	}
	m.sessions = make(map[int64]*Session)
	m.mu.Unlock()
	for _, s := range all {
		s.Close()
	}
	return nil
}

type Session struct {
	dataScope    string
	schema       schemaGate
	strategy     dialectStrategy
	db           *sql.DB
	ctx          context.Context
	cancel       context.CancelFunc
	kind         connections.Kind
	id           int64
	generation   uint64
	catalog      string
	mu           sync.Mutex
	closed       bool
	leases       map[*Lease]bool
	transactions map[*Tx]bool
}

func (s *Session) ProfileID() int64   { return s.id }
func (s *Session) Generation() uint64 { return s.generation }
func (s *Session) Catalog() string    { return s.strategy.catalog(s.catalog) }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(s.kind)
	caps := d.Capabilities
	if sqlite, ok := s.strategy.(sqliteDialect); ok && sqlite.options.ReadOnly {
		caps.EditableTables = false
	}
	return caps
}
func (s *Session) isClosed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	leases := make([]*Lease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	txs := make([]*Tx, 0, len(s.transactions))
	for t := range s.transactions {
		txs = append(txs, t)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.Close()
	}
	for _, t := range txs {
		t.Close()
	}
	return s.db.Close()
}
func (s *Session) check(o contract.ObjectRef) error {
	if s.isClosed() {
		return ErrRevoked
	}
	if o.ProfileID != s.id || o.Catalog != s.Catalog() || o.Schema == "" || o.Name == "" {
		return errors.New("object identity does not match session")
	}
	if err := s.strategy.namespace(o.Schema); err != nil {
		return err
	}
	return nil
}
func quote(v string) string                 { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func qualified(o contract.ObjectRef) string { return quote(o.Schema) + "." + quote(o.Name) }
func (s *Session) arg(i int) string         { return s.strategy.argument(i) }
func (s *Session) Reserve(ctx context.Context) (contract.SQLLease, error) {
	if s.kind == connections.SQLite {
		child, done := s.requestContext(ctx)
		defer done()
		ctx = child
		release, err := s.schema.acquire(ctx, false)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if s.isClosed() {
		return nil, ErrRevoked
	}
	c, e := s.db.Conn(ctx)
	if e != nil {
		return nil, e
	}
	leasectx, leasecancel := context.WithCancel(s.ctx)
	l := &Lease{s: s, c: c, ctx: leasectx, cancel: leasecancel}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		c.Close()
		return nil, ErrRevoked
	}
	s.leases[l] = true
	return l, nil
}

type Lease struct {
	ctx    context.Context
	cancel context.CancelFunc
	ops    sync.Mutex
	s      *Session
	c      *sql.Conn
	mu     sync.Mutex
	closed bool
}

func (l *Lease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	_ = l.c.Raw(func(any) error { return driver.ErrBadConn })
	err := l.c.Close()
	if errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
func (l *Lease) Execute(ctx context.Context, q string) (contract.SQLResult, error) {
	return l.Stream(ctx, q, nil)
}
func (l *Lease) Stream(ctx context.Context, q string, sink contract.StreamSink) (contract.SQLResult, error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || l.s.isClosed() {
		return contract.SQLResult{}, ErrRevoked
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	defer func() { stop(); cancel() }()
	if l.s.kind == connections.SQLite {
		release, err := l.s.schema.acquire(ctx, false)
		if err != nil {
			return contract.SQLResult{}, err
		}
		defer release()
	}
	start := time.Now()
	r, e := execute(ctx, l.c, l.s.kind, q, sink)
	r.Duration = time.Since(start)
	r.SetLegacyView()
	e = l.connectionError(ctx, e)
	return r, e
}

func (s *Session) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }
}

func (l *Lease) SetSchema(ctx context.Context, schema string) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || l.s.isClosed() {
		return contract.ErrRevoked
	}
	if schema == "" {
		return nil
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	defer func() { stop(); cancel() }()
	return l.connectionError(child, l.s.strategy.setSchema(child, l.c, schema))
}

func (l *Lease) connectionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	lost := errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || (pgconn.Timeout(err) && ctx.Err() == nil)
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		lost = lost || pg.Severity == "FATAL" || pg.Severity == "PANIC" || strings.HasPrefix(pg.Code, "08")
	}
	if lost {
		_ = l.Close()
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}
