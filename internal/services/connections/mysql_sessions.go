package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrMySQLNotConnected = errors.New("MySQL session not connected")
var ErrMySQLManagerClosed = errors.New("MySQL session manager closed")

// MySQLSessionManager owns authenticated connections and cancels in-flight
// requests on Disconnect. Only explicit Connect methods initiate network I/O.
type MySQLSessionManager struct {
	mu       sync.Mutex
	sessions map[int64]*mysqlManagedSession
	versions map[int64]uint64
	pending  map[int64]context.CancelFunc
	closed   bool
	opener   func(context.Context, ActiveSettings, string) (*sql.DB, error)
}
type mysqlManagedSession struct {
	db        *sql.DB
	sqlDB     *sql.DB
	ctx       context.Context
	cancel    context.CancelFunc
	settings  ActiveSettings

	sqlTabLimit int

	sqlTabsMu sync.Mutex
	sqlTabs   map[uint64]*mysqlSQLTabLease
}

func NewMySQLSessionManager() *MySQLSessionManager {
	return &MySQLSessionManager{
		sessions: make(map[int64]*mysqlManagedSession),
		versions: make(map[int64]uint64),
		pending:  make(map[int64]context.CancelFunc),
	}
}

// Connect replaces any previous connection with the same profile ID.
// Generation checks ensure that a canceled late handshake cannot resurrect it.
func (m *MySQLSessionManager) Connect(ctx context.Context, s ActiveSettings, password string) error {
	if s.ProfileID <= 0 {
		return fmt.Errorf("%w: missing profile ID", ErrInvalid)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrMySQLManagerClosed
	}
	m.versions[s.ProfileID]++
	token := m.versions[s.ProfileID]
	previous := m.sessions[s.ProfileID]
	previousPending := m.pending[s.ProfileID]
	delete(m.sessions, s.ProfileID)
	connectCtx, cancel := context.WithCancel(ctx)
	m.pending[s.ProfileID] = cancel
	m.mu.Unlock()
	if previousPending != nil {
		previousPending()
	}
	if previous != nil {
		previous.cancel()
		_ = previous.close()
	}
	var db, sqlDB *sql.DB
	var err error
	if m.opener != nil {
		db, err = m.opener(connectCtx, s, password)
		sqlDB = db // injected mock pool in unit tests
	} else {
		db, sqlDB, err = OpenDirectMySQLPools(connectCtx, s, password)
	}
	if err != nil {
		cancel()
		m.mu.Lock()
		if m.versions[s.ProfileID] == token {
			delete(m.pending, s.ProfileID)
		}
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	stale := m.closed || m.versions[s.ProfileID] != token || connectCtx.Err() != nil
	var managed *mysqlManagedSession
	if !stale {
		delete(m.pending, s.ProfileID)
		managed = &mysqlManagedSession{
			db: db, sqlDB: sqlDB, ctx: connectCtx, cancel: cancel,
			settings: s, sqlTabLimit: mysqlSQLTabLimit(s.MySQLAdvanced),
			sqlTabs:  make(map[uint64]*mysqlSQLTabLease),
		}
		m.sessions[s.ProfileID] = managed
	}
	m.mu.Unlock()
	if stale {
		cancel()
		_ = db.Close()
		if sqlDB != db {
			_ = sqlDB.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return context.Canceled
	}
	if s.MySQLAdvanced.KeepAlive {
		go managed.runKeepAlive()
	}
	return nil
}

func (m *MySQLSessionManager) IsConnected(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[id]
	return ok
}

func (m *MySQLSessionManager) Disconnect(id int64) error {
	m.mu.Lock()
	m.versions[id]++
	old := m.sessions[id]
	cancelPending := m.pending[id]
	delete(m.pending, id)
	delete(m.sessions, id)
	m.mu.Unlock()
	if cancelPending != nil {
		cancelPending()
	}
	if old == nil {
		return nil
	}
	old.cancel()
	return old.close()
}

func (m *MySQLSessionManager) Close() error {
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	pending := m.pending
	m.pending = make(map[int64]context.CancelFunc)
	m.sessions = make(map[int64]*mysqlManagedSession)
	for id := range sessions {
		m.versions[id]++
	}
	for id := range pending {
		m.versions[id]++
	}
	m.mu.Unlock()
	for _, cancel := range pending {
		cancel()
	}
	var err error
	for _, s := range sessions {
		s.cancel()
		err = errors.Join(err, s.close())
	}
	return err
}

// close revokes tab leases before shutting down both pools. Drivers may still
// be acknowledging canceled operations, so lease cleanup runs asynchronously.
func (s *mysqlManagedSession) close() error {
	s.sqlTabsMu.Lock()
	tabs := s.sqlTabs
	s.sqlTabs = nil
	s.sqlTabsMu.Unlock()
	for _, lease := range tabs {
		lease.cancel()
		go lease.close()
	}
	var err error
	if s.sqlDB != nil && s.sqlDB != s.db {
		err = s.sqlDB.Close()
	}
	return errors.Join(err, s.db.Close())
}

// Databases is a real metadata query on the authenticated session, scoped to
// the connection's lifetime. The caller receives only database names.
func (m *MySQLSessionManager) Databases(ctx context.Context, id int64) ([]string, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	rows, err := s.db.QueryContext(queryCtx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.ctx.Err() != nil {
		return nil, context.Canceled
	}
	return names, nil
}
