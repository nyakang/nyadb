package mysql

import (
	"context"
	"time"
)

const mysqlKeepAlivePingTimeout = 5 * time.Second

// runKeepAlive is an independent NyaDB policy, not a verified Navicat
// implementation. A tick first checks the existing SQL-tab connections and
// then the idle metadata pool. It never executes user SQL or reconnects a tab.
func (s *mysqlManagedSession) runKeepAlive() {
	seconds := s.settings.MySQLAdvanced.KeepAliveSeconds
	if seconds < 1 || s.ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.ctx.Err() != nil {
				return
			}
			s.keepAliveOnce()
		}
	}
}

// keepAliveOnce is also called directly by deterministic tests. A SQL tab
// whose query currently holds its lease lock is skipped without waiting;
// the next tick may probe it after the query finishes.
func (s *mysqlManagedSession) keepAliveOnce() {
	if s.ctx.Err() != nil {
		return
	}
	s.sqlTabsMu.Lock()
	tabs := make([]*mysqlSQLTabLease, 0, len(s.sqlTabs))
	for _, lease := range s.sqlTabs {
		tabs = append(tabs, lease)
	}
	s.sqlTabsMu.Unlock()

	for _, lease := range tabs {
		if s.ctx.Err() != nil {
			return
		}
		s.pingIdleSQLTab(lease)
	}
	// The metadata/data pool is stateless and may replace a broken idle
	// connection. Skip it when no physical connection is idle: PingContext
	// otherwise could establish a new connection solely for keepalive.
	if s.ctx.Err() == nil && s.db != nil && s.db.Stats().Idle > 0 {
		ctx, cancel := context.WithTimeout(s.ctx, mysqlKeepAlivePingTimeout)
		_ = s.db.PingContext(ctx)
		cancel()
	}
}

func (s *mysqlManagedSession) pingIdleSQLTab(lease *mysqlSQLTabLease) {
	if !lease.mu.TryLock() {
		return
	}
	defer lease.mu.Unlock()
	if s.ctx.Err() != nil || lease.ctx.Err() != nil || lease.closed || lease.lost || lease.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(lease.ctx, mysqlKeepAlivePingTimeout)
	err := lease.conn.PingContext(ctx)
	cancel()
	if err == nil || s.ctx.Err() != nil || lease.ctx.Err() != nil {
		return
	}
	// Never substitute a fresh connection for an SQL editor that may hold
	// user variables, temporary tables or an uncommitted transaction.
	lease.lost = true
	lease.cancel()
	conn := lease.conn
	lease.conn = nil
	go func() { _ = conn.Close() }()
}
