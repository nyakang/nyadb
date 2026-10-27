package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrMySQLSQLSessionLost means that a tab's physical SQL session may no longer
// hold its user variables, temporary tables, or uncommitted transaction.
// Never silently recreate such a session and pretend the state is preserved.
var ErrMySQLSQLSessionLost = errors.New("MySQL SQL tab session lost; open a new query tab")

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
	m.mu.Unlock()
	if s == nil {
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
	m.mu.Unlock()
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
		leaseCtx, cancel := context.WithCancel(s.ctx)
		lease = &mysqlSQLTabLease{ctx: leaseCtx, cancel: cancel}
		s.sqlTabs[tabID] = lease
	}
	s.sqlTabsMu.Unlock()

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
	if queryCtx.Err() != nil || errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) {
		// MySQL cancellation may close the TCP connection while server-side
		// writes have already committed. Do not silently swap sessions.
		lease.lost = true
		lease.cancel()
		_ = lease.conn.Close()
		lease.conn = nil
	}
	return result, err
}

func hasSQLStatement(statement string) bool {
	for _, r := range statement {
		if r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
	}
	return false
}
