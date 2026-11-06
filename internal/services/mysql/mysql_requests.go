package mysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlProductKey struct{}

func mysqlWarningsSQL(ctx context.Context) string {
	if ctx.Value(mysqlProductKey{}) == connections.TiDB {
		return "SHOW WARNINGS"
	}
	return "SHOW WARNINGS LIMIT 1"
}

func mysqlValueWarning(ctx context.Context, code int, message string) bool {
	// TiDB reports a prepared-plan cache miss as a warning, even when the
	// statement preserves every value. Other warnings still reject the batch.
	return !(ctx.Value(mysqlProductKey{}) == connections.TiDB && code == 1105 && strings.HasPrefix(message, "skip prepared plan-cache:"))
}

func mysqlReadOptions(kind connections.Kind) *sql.TxOptions {
	return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: kind != connections.TiDB}
}

// Grid writes need locking reads and READ COMMITTED. Bind that contract on
// the private metadata connection, independently of global defaults and SQL tabs.
func mysqlPrepareWrite(ctx context.Context, conn *sql.Conn) error {
	if ctx.Value(mysqlProductKey{}) == connections.TiDB {
		_, err := conn.ExecContext(ctx, "SET SESSION tidb_txn_mode='pessimistic'")
		return err
	}
	return nil
}

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
	requestCtx = context.WithValue(requestCtx, mysqlProductKey{}, session.settings.Kind)
	stop := context.AfterFunc(session.ctx, cancel)
	return session, requestCtx, func() { stop(); cancel() }, nil
}
