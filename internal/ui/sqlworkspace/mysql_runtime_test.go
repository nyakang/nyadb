package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"io"
)

// MySQLRuntime adapts the existing service without exposing its tokens to shell.
func MySQLRuntime(manager *mysqlsvc.MySQLSessionManager) Runtime {
	if manager == nil {
		return nil
	}
	return mySQLRuntime{manager: manager}
}

type mySQLRuntime struct{ manager *mysqlsvc.MySQLSessionManager }

func (r mySQLRuntime) IsConnected(id int64) bool { return r.manager.IsConnected(id) }
func (r mySQLRuntime) Reserve(id int64, tabID uint64) (Lease, error) {
	token, err := r.manager.ReserveSQLTabToken(id, tabID)
	if err != nil {
		return nil, err
	}
	return &mySQLLease{manager: r.manager, token: token}, nil
}

type mySQLLease struct {
	manager *mysqlsvc.MySQLSessionManager
	token   mysqlsvc.MySQLSQLTabToken
}

func (l *mySQLLease) Preconnect(ctx context.Context) error {
	return l.manager.PreconnectSQLTabToken(ctx, l.token)
}
func (l *mySQLLease) Execute(ctx context.Context, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
	return l.manager.ExecuteSQLTabToken(ctx, l.token, database, statement)
}
func (l *mySQLLease) Replace() (Lease, error) {
	token, err := l.manager.ReplaceSQLTabToken(l.token)
	if err != nil {
		return nil, err
	}
	return &mySQLLease{manager: l.manager, token: token}, nil
}
func (l *mySQLLease) Release() { l.manager.ReleaseSQLTabToken(l.token) }

func (l *mySQLLease) ExecuteFile(ctx context.Context, db string, reader io.Reader) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabTokenFile(ctx, l.token, db, reader)
}
func (l *mySQLLease) ExecuteStream(ctx context.Context, db, sql string, sink sqlresult.Sink) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabTokenStream(ctx, l.token, db, sql, sink)
}
