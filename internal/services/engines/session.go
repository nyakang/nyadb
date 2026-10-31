// Package engines implements PostgreSQL and user SQLite sessions. Configuration
// storage uses a separate modernc handle owned by localdb.
package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	_ "modernc.org/sqlite"
)

var ErrRevoked = errors.New("database session revoked; reconnect explicitly")
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
	var db *sql.DB
	if p.Kind == connections.PostgreSQL {
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/" + p.Database, User: url.UserPassword(p.Username, password)}
		q := u.Query()
		mode := p.TLSMode
		if mode == "" {
			mode = "require"
		}
		switch mode {
		case "disable", "prefer", "require", "verify-ca", "verify-full":
		default:
			return errors.New("unsupported PostgreSQL TLS mode")
		}
		q.Set("sslmode", mode)
		q.Set("connect_timeout", "10")
		u.RawQuery = q.Encode()
		cfg, e := pgx.ParseConfig(u.String())
		if e != nil {
			return errors.New("invalid PostgreSQL configuration")
		}
		if m.DialContext != nil {
			cfg.DialFunc = m.DialContext
		}
		db = stdlib.OpenDB(*cfg)
	} else {
		abs, e := filepath.Abs(p.Database)
		if e != nil {
			return e
		}
		u := url.URL{Scheme: "file", Path: abs}
		q := u.Query()
		q.Add("_pragma", "busy_timeout(1000)")
		q.Add("_pragma", "foreign_keys(1)")
		u.RawQuery = q.Encode()
		var e2 error
		db, e2 = sql.Open("sqlite", u.String())
		if e2 != nil {
			return e2
		}
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
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
	s := &Session{db: db, kind: p.Kind, id: p.ID, generation: intent, ctx: sessionCtx, cancel: sessionCancel, catalog: p.Database, leases: make(map[*Lease]bool), transactions: make(map[*Tx]bool)}
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
func (s *Session) Catalog() string {
	if s.kind == connections.SQLite {
		return "main"
	}
	return s.catalog
}
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Schemas: true, Transactions: true, EditableTables: true, Explain: true}
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
	if s.kind == connections.SQLite && o.Schema != "main" {
		return errors.New("SQLite supports main schema only")
	}
	return nil
}
func quote(v string) string                 { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func qualified(o contract.ObjectRef) string { return quote(o.Schema) + "." + quote(o.Name) }
func (s *Session) arg(i int) string {
	if s.kind == connections.PostgreSQL {
		return "$" + strconv.Itoa(i)
	}
	return "?"
}
func (s *Session) Reserve(ctx context.Context) (contract.SQLLease, error) {
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
	start := time.Now()
	r, e := execute(ctx, l.c, l.s.kind, q, sink)
	r.Duration = time.Since(start)
	r.SetLegacyView()
	return r, e
}

func (s *Session) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }
}

func (l *Lease) SetSchema(ctx context.Context, schema string) error {
	if schema == "" {
		return nil
	}
	if l.s.kind == connections.SQLite {
		if schema != "main" {
			return errors.New("SQLite only supports main")
		}
		return nil
	}
	l.ops.Lock()
	defer l.ops.Unlock()
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || l.s.isClosed() {
		return ErrRevoked
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	defer func() { stop(); cancel() }()
	_, e := l.c.ExecContext(ctx, "SET search_path TO "+quote(schema))
	return e
}
