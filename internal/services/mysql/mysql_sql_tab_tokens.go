package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"io"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// MySQLSQLTabToken binds one tab to one particular authenticated profile
// generation AND one lease identity. A stale token cannot reach a reconnected
// profile even when the numeric tab ID is reused.
type MySQLSQLTabToken struct {
	profileID  int64
	tabID      uint64
	generation uint64
	lease      *mysqlSQLTabLease
}

func (token MySQLSQLTabToken) IsZero() bool { return token.lease == nil }

// ReserveSQLTabToken registers the tab on the current profile session.
// It performs no network I/O and should be called on the native UI thread.
func (m *MySQLSessionManager) ReserveSQLTabToken(profileID int64, tabID uint64) (MySQLSQLTabToken, error) {
	if tabID == 0 {
		return MySQLSQLTabToken{}, fmt.Errorf("%w: missing SQL tab ID", connections.ErrInvalid)
	}
	m.mu.Lock()
	s, generation := m.sessions[profileID], m.versions[profileID]
	m.mu.Unlock()
	if s == nil || s.ctx.Err() != nil {
		return MySQLSQLTabToken{}, ErrMySQLNotConnected
	}
	s.sqlTabsMu.Lock()
	defer s.sqlTabsMu.Unlock()
	if s.sqlTabs == nil || s.ctx.Err() != nil {
		return MySQLSQLTabToken{}, ErrMySQLNotConnected
	}
	lease := s.sqlTabs[tabID]
	if lease == nil {
		if len(s.sqlTabs) >= s.sqlTabLimit {
			return MySQLSQLTabToken{}, fmt.Errorf("%w: at most %d SQL tab sessions per connection", ErrMySQLSQLSessionLimit, s.sqlTabLimit)
		}
		leaseCtx, cancel := context.WithCancel(s.ctx)
		lease = &mysqlSQLTabLease{ctx: leaseCtx, cancel: cancel}
		s.sqlTabs[tabID] = lease
	}
	// A concurrent Connect can invalidate this token, but it cannot cause
	// the token to point at the replacement session or lease.
	return MySQLSQLTabToken{profileID: profileID, tabID: tabID, generation: generation, lease: lease}, nil
}

func (m *MySQLSessionManager) checkedSQLTabToken(token MySQLSQLTabToken) (*mysqlManagedSession, *mysqlSQLTabLease, error) {
	if token.IsZero() {
		return nil, nil, ErrMySQLSQLSessionLost
	}
	m.mu.Lock()
	s := m.sessions[token.profileID]
	if s == nil || m.versions[token.profileID] != token.generation || s.ctx.Err() != nil {
		m.mu.Unlock()
		return nil, nil, ErrMySQLSQLSessionLost
	}
	s.sqlTabsMu.Lock()
	lease := s.sqlTabs[token.tabID]
	if lease != token.lease {
		s.sqlTabsMu.Unlock()
		m.mu.Unlock()
		return nil, nil, ErrMySQLSQLSessionLost
	}
	s.sqlTabsMu.Unlock()
	m.mu.Unlock()
	return s, lease, nil
}

// PreconnectSQLTabToken uses the exact reserved lease. It cannot accidentally
// preconnect a new profile when the old tab's worker finishes after reconnect.
func (m *MySQLSessionManager) PreconnectSQLTabToken(ctx context.Context, token MySQLSQLTabToken) error {
	s, lease, err := m.checkedSQLTabToken(token)
	if err != nil {
		return err
	}
	return preconnectSQLTabLease(ctx, s, lease)
}

func (m *MySQLSessionManager) ExecuteSQLTabToken(ctx context.Context, token MySQLSQLTabToken, database, statement string) (MySQLSQLResult, error) {
	if !hasSQLStatement(statement) {
		return MySQLSQLResult{}, ErrEmptySQL
	}
	if err := validSQLSchema(database); err != nil {
		return MySQLSQLResult{}, err
	}
	s, lease, err := m.checkedSQLTabToken(token)
	if err != nil {
		return MySQLSQLResult{}, err
	}
	return executeSQLTabLease(ctx, s, lease, database, statement)
}

// ReplaceSQLTabToken is an explicit, no-network recovery operation after a
// user acknowledges that the old tab's transaction, variables and temporary
// tables are irrecoverable. It atomically replaces only the matching lease,
// retaining the tab's quota slot. It NEVER runs or replays SQL.
//
// A stale token (including one from a disconnected profile generation) cannot
// replace the lease of a newly connected session. In that case callers may
// explicitly reserve a new token after verifying the original profile.
func (m *MySQLSessionManager) ReplaceSQLTabToken(old MySQLSQLTabToken) (MySQLSQLTabToken, error) {
	if old.IsZero() {
		return MySQLSQLTabToken{}, ErrMySQLSQLSessionLost
	}
	m.mu.Lock()
	s := m.sessions[old.profileID]
	if s == nil || m.versions[old.profileID] != old.generation || s.ctx.Err() != nil {
		m.mu.Unlock()
		return MySQLSQLTabToken{}, ErrMySQLSQLSessionLost
	}
	s.sqlTabsMu.Lock()
	lease := s.sqlTabs[old.tabID]
	if lease != old.lease || s.sqlTabs == nil {
		s.sqlTabsMu.Unlock()
		m.mu.Unlock()
		return MySQLSQLTabToken{}, ErrMySQLSQLSessionLost
	}
	leaseCtx, cancel := context.WithCancel(s.ctx)
	replacement := &mysqlSQLTabLease{ctx: leaseCtx, cancel: cancel}
	s.sqlTabs[old.tabID] = replacement
	s.sqlTabsMu.Unlock()
	m.mu.Unlock()
	// Revocation is immediate, while driver cleanup remains asynchronous.
	lease.cancel()
	go lease.close()
	return MySQLSQLTabToken{
		profileID: old.profileID, tabID: old.tabID,
		generation: old.generation, lease: replacement,
	}, nil
}

// ReleaseSQLTabToken affects only its matching lease. A stale close event
// never revokes a newly-created tab with the same numeric ID.
func (m *MySQLSessionManager) ReleaseSQLTabToken(token MySQLSQLTabToken) {
	if token.IsZero() {
		return
	}
	m.mu.Lock()
	s := m.sessions[token.profileID]
	if s == nil || m.versions[token.profileID] != token.generation {
		m.mu.Unlock()
		return
	}
	s.sqlTabsMu.Lock()
	lease := s.sqlTabs[token.tabID]
	if lease == token.lease {
		delete(s.sqlTabs, token.tabID)
	}
	s.sqlTabsMu.Unlock()
	m.mu.Unlock()
	if lease == token.lease {
		lease.cancel()
		go lease.close()
	}
}

func (m *MySQLSessionManager) ExecuteSQLTabTokenStream(ctx context.Context, token MySQLSQLTabToken, database, statement string, sink sqlresult.Sink) (MySQLSQLResult, error) {
	if !hasSQLStatement(statement) {
		return MySQLSQLResult{}, ErrEmptySQL
	}
	if e := validSQLSchema(database); e != nil {
		return MySQLSQLResult{}, e
	}
	s, lease, e := m.checkedSQLTabToken(token)
	if e != nil {
		return MySQLSQLResult{}, e
	}
	return executeSQLTabLease(ctx, s, lease, database, statement, sink)
}

// ExecuteSQLTabTokenFile scans the stream on the tab's original connection.
// Earlier completed statements remain visible when a later statement fails.
func (m *MySQLSessionManager) ExecuteSQLTabTokenFile(ctx context.Context, token MySQLSQLTabToken, database string, reader io.Reader) (MySQLSQLResult, error) {
	if reader == nil {
		return MySQLSQLResult{}, ErrEmptySQL
	}
	if err := validSQLSchema(database); err != nil {
		return MySQLSQLResult{}, err
	}
	s, lease, err := m.checkedSQLTabToken(token)
	if err != nil {
		return MySQLSQLResult{}, err
	}
	return executeSQLTabWork(ctx, s, lease, func(qctx context.Context, c *sql.Conn) (MySQLSQLResult, error) {
		if database != "" {
			if _, e := c.ExecContext(qctx, "USE "+quoteSQLSchema(database)); e != nil {
				return MySQLSQLResult{}, e
			}
		}
		return executeSQLScriptOnConn(qctx, c, reader, nil)
	})
}
