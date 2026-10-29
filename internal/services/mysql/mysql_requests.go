package mysql

import (
	"context"
	"time"
)

// requestContext binds an operation to its caller and authenticated session.
// A zero timeout leaves the deadline to the caller. Session lookup is the only
// part that holds the manager lock; requests and cleanup run independently.
func (m *MySQLSessionManager) requestContext(ctx context.Context, profileID int64, timeout time.Duration) (*mysqlManagedSession, context.Context, context.CancelFunc, error) {
	m.mu.Lock()
	session := m.sessions[profileID]
	m.mu.Unlock()
	if session == nil {
		return nil, nil, nil, ErrMySQLNotConnected
	}

	var requestCtx context.Context
	var cancel context.CancelFunc
	if timeout == 0 {
		requestCtx, cancel = context.WithCancel(ctx)
	} else {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	stop := context.AfterFunc(session.ctx, cancel)
	return session, requestCtx, func() { stop(); cancel() }, nil
}
