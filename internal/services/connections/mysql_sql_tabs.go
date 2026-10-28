package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"

	mysql "github.com/go-sql-driver/mysql"
)

// ErrMySQLSQLSessionLost means that a tab's physical SQL session may no longer
// hold its user variables, temporary tables, or uncommitted transaction.
// Never silently recreate such a session and pretend the state is preserved.
var ErrMySQLSQLSessionLost = errors.New("MySQL SQL tab session lost; open a new query tab")

// ErrMySQLSQLSessionLimit indicates that the per-profile SQL-tab budget is
// exhausted. It does not mean the metadata pool or whole profile is full.
var ErrMySQLSQLSessionLimit = errors.New("MySQL SQL tab session limit reached")

type mysqlSQLTabLease struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	conn   *sql.Conn
	lost   bool
	closed bool
}

func (lease *mysqlSQLTabLease) close() {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.closed = true
	if lease.conn != nil {
		_ = lease.conn.Close()
		lease.conn = nil
	}
}

// ReleaseSQLTab ends one tab's physical connection. It is safe to call after
// a tab is disposed or a profile has disconnected. Revocation is immediate;
// driver cleanup is asynchronous so the native UI never blocks on a query.
func (m *MySQLSessionManager) ReleaseSQLTab(profileID int64, tabID uint64) {
	m.mu.Lock()
	s := m.sessions[profileID]
	generation := m.versions[profileID]
	m.mu.Unlock()
	// Unversioned legacy calls must not revoke a new session's lease.
	if s == nil || generation != 1 {
		return
	}
	s.sqlTabsMu.Lock()
	lease := s.sqlTabs[tabID]
	delete(s.sqlTabs, tabID)
	s.sqlTabsMu.Unlock()
	if lease != nil {
		lease.cancel()
		go lease.close()
	}
}

// ReserveSQLTab registers a tab before any network I/O. A tab closed before
// its worker starts can therefore revoke the reserved lease.
func (m *MySQLSessionManager) ReserveSQLTab(profileID int64, tabID uint64) error {
	if tabID == 0 {
		return fmt.Errorf("%w: missing SQL tab ID", ErrInvalid)
	}
	m.mu.Lock()
	s := m.sessions[profileID]
	generation := m.versions[profileID]
	m.mu.Unlock()
	if generation != 1 && s != nil {
		return ErrMySQLSQLSessionLost
	}
	if s == nil || s.ctx.Err() != nil {
		return ErrMySQLNotConnected
	}
	s.sqlTabsMu.Lock()
	defer s.sqlTabsMu.Unlock()
	if s.sqlTabs == nil || s.ctx.Err() != nil {
		return ErrMySQLNotConnected
	}
	if s.sqlTabs[tabID] == nil {
		if len(s.sqlTabs) >= s.sqlTabLimit {
			return fmt.Errorf("%w: at most %d SQL tab sessions per connection", ErrMySQLSQLSessionLimit, s.sqlTabLimit)
		}
		leaseCtx, cancel := context.WithCancel(s.ctx)
		s.sqlTabs[tabID] = &mysqlSQLTabLease{ctx: leaseCtx, cancel: cancel}
	}
	return nil
}

// PreconnectSQLTab reserves the tab's physical connection without executing
// SQL. Run it on a worker: Conn can block on dial or pool capacity. Unlike
// ExecuteSQLTab, it never recreates a released lease.
func (m *MySQLSessionManager) PreconnectSQLTab(ctx context.Context, profileID int64, tabID uint64) error {
	if tabID == 0 {
		return fmt.Errorf("%w: missing SQL tab ID", ErrInvalid)
	}
	m.mu.Lock()
	s := m.sessions[profileID]
	generation := m.versions[profileID]
	m.mu.Unlock()
	if generation != 1 && s != nil {
		return ErrMySQLSQLSessionLost
	}
	if s == nil || s.ctx.Err() != nil {
		return ErrMySQLNotConnected
	}
	s.sqlTabsMu.Lock()
	lease := s.sqlTabs[tabID]
	s.sqlTabsMu.Unlock()
	if lease == nil {
		return ErrMySQLSQLSessionLost
	}
	return preconnectSQLTabLease(ctx, s, lease)
}

func preconnectSQLTabLease(ctx context.Context, s *mysqlManagedSession, lease *mysqlSQLTabLease) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed || lease.lost || lease.ctx.Err() != nil {
		return ErrMySQLSQLSessionLost
	}
	if lease.conn != nil {
		return nil
	}
	connectCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lease.ctx, cancel)
	defer func() { stop(); cancel() }()
	conn, err := s.sqlDB.Conn(connectCtx)
	if err != nil {
		if connectCtx.Err() != nil {
			lease.lost = true
		}
		return err
	}
	if connectCtx.Err() != nil || lease.ctx.Err() != nil {
		_ = conn.Close()
		lease.lost = true
		return context.Canceled
	}
	lease.conn = conn
	return nil
}

// ExecuteSQLTab holds the same physical MySQL connection across executions
// for a single SQL editor. Different tab IDs have separate leases; metadata
// uses s.db while this function uses s.sqlDB. A canceled or broken lease is
// permanently invalid and must never be transparently replaced.
func (m *MySQLSessionManager) ExecuteSQLTab(ctx context.Context, profileID int64, tabID uint64, database, statement string) (result MySQLSQLResult, err error) {
	if tabID == 0 {
		return result, fmt.Errorf("%w: missing SQL tab ID", ErrInvalid)
	}
	if statement == "" || !hasSQLStatement(statement) {
		return result, ErrEmptySQL
	}
	if err := validSQLSchema(database); err != nil {
		return result, err
	}
	m.mu.Lock()
	s := m.sessions[profileID]
	generation := m.versions[profileID]
	m.mu.Unlock()
	if generation != 1 && s != nil {
		return result, ErrMySQLSQLSessionLost
	}
	if s == nil || s.ctx.Err() != nil {
		return result, ErrMySQLNotConnected
	}
	s.sqlTabsMu.Lock()
	if s.sqlTabs == nil || s.ctx.Err() != nil {
		s.sqlTabsMu.Unlock()
		return result, ErrMySQLNotConnected
	}
	lease := s.sqlTabs[tabID]
	if lease == nil {
		if len(s.sqlTabs) >= s.sqlTabLimit {
			s.sqlTabsMu.Unlock()
			return result, fmt.Errorf("%w: at most %d SQL tab sessions per connection", ErrMySQLSQLSessionLimit, s.sqlTabLimit)
		}
		leaseCtx, cancel := context.WithCancel(s.ctx)
		lease = &mysqlSQLTabLease{ctx: leaseCtx, cancel: cancel}
		s.sqlTabs[tabID] = lease
	}
	s.sqlTabsMu.Unlock()

	return executeSQLTabLease(ctx, s, lease, database, statement)
}

func executeSQLTabLease(ctx context.Context, s *mysqlManagedSession, lease *mysqlSQLTabLease, database, statement string) (result MySQLSQLResult, err error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed || lease.lost || lease.ctx.Err() != nil {
		return result, ErrMySQLSQLSessionLost
	}
	queryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lease.ctx, cancel)
	defer func() { stop(); cancel() }()

	if lease.conn == nil {
		conn, err := s.sqlDB.Conn(queryCtx)
		if err != nil {
			if queryCtx.Err() != nil {
				lease.lost = true
			}
			return result, err
		}
		lease.conn = conn
	}
	result, err = executeSQLOnConn(queryCtx, lease.conn, database, statement)
	if queryCtx.Err() != nil || mysqlSQLSessionBroken(err) {
		// MySQL cancellation may close the TCP connection while server-side
		// writes have already committed. Do not silently swap sessions.
		lease.lost = true
		lease.cancel()
		_ = lease.conn.Close()
		lease.conn = nil
		if queryCtx.Err() == nil && err != nil {
			// Surface the lost-state warning on the first failed query, not
			// only on the next attempt. Retain the original error for logs.
			err = fmt.Errorf("%w: %w", ErrMySQLSQLSessionLost, err)
		}
	}
	return result, err
}

// MySQL 8.4 returns server error 4031 after wait_timeout expiry rather than
// driver.ErrBadConn. Both mean the stateful SQL tab's original transaction,
// variables and temporary tables are gone. Ordinary syntax/permission
// errors must NOT force a new session.
func mysqlSQLSessionBroken(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	var serverErr *mysql.MySQLError
	if errors.As(err, &serverErr) {
		return serverErr.Number == 4031 || serverErr.Number == 2006 ||
			serverErr.Number == 2013 || serverErr.Number == 2055
	}
	return false
}

func hasSQLStatement(statement string) bool {
	for _, r := range statement {
		if r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
	}
	return false
}
