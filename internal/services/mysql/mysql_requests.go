package mysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlProductKey struct{}

func mysqlFingerprintExpression(ctx context.Context, quoted string) string {
	value := "CAST(" + quoted + " AS BINARY)"
	if ctx.Value(mysqlProductKey{}) == connections.Dolt {
		// Dolt's SHA2 coerces its input through UTF-8. Hash the reversible
		// ASCII hex encoding so arbitrary BLOB bytes remain distinguishable.
		// Both page snapshots and locking verification use this expression.
		value = "HEX(" + value + ")"
	}
	return "SHA2(" + value + ",256)"
}

func mysqlBinarySubstring(ctx context.Context, value, offset, length string) string {
	if ctx.Value(mysqlProductKey{}) == connections.Dolt {
		// SUBSTRING on a Dolt BLOB decodes it as UTF-8. Slice the reversible
		// ASCII hex representation and return bytes, retaining byte limits.
		return "UNHEX(SUBSTRING(HEX(" + value + "),((" + offset + ")-1)*2+1,(" + length + ")*2))"
	}
	return "SUBSTRING(" + value + "," + offset + "," + length + ")"
}

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
