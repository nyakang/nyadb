package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// startMySQLSQLPreconnect never blocks the native UI and never executes SQL.
func (s *State) startMySQLSQLPreconnect(t *tab, lease mysqlsvc.MySQLSQLTabToken) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t.sqlPrepareCancel = cancel
	t.sqlPrepareResults = done
	manager := s.sessions.Manager
	invalidate := s.mysqlSQLPreconnectInvalidate
	go func() {
		done <- manager.PreconnectSQLTabToken(ctx, lease)
		if invalidate != nil {
			invalidate()
		}
	}()
}

// recoverMySQLSQLTab is only called after the user explicitly acknowledges
// that the original SQL session is gone. It creates a new lease for the SAME
// profile and tab, retaining authored SQL but never replaying any statement.
// The original profile must already be connected by the user.
func (s *State) recoverMySQLSQLTab(t *tab) {
	if t == nil || t.kind != tabSQL || !t.sqlRecoveryNeeded ||
		t.sqlLoading || t.sqlPrepareResults != nil {
		return
	}
	id := t.sqlRecoveryProfile
	manager := s.sessions.Manager
	if id <= 0 || manager == nil || !manager.IsConnected(id) ||
		s.sessions.StatusFor(id) != "已连接" {
		t.sqlError = "请先连接原 MySQL 配置，再选择以新会话继续"
		s.status = t.sqlError
		return
	}
	var (
		lease mysqlsvc.MySQLSQLTabToken
		err   error
	)
	if t.sqlLease.IsZero() {
		lease, err = manager.ReserveSQLTabToken(id, t.id)
	} else {
		lease, err = manager.ReplaceSQLTabToken(t.sqlLease)
		if errors.Is(err, mysqlsvc.ErrMySQLSQLSessionLost) {
			// The profile was explicitly reconnected since this tab was
			// created; a stale generation may not be used to replace the
			// new profile's leases. The tab ID is unique to this workspace.
			lease, err = manager.ReserveSQLTabToken(id, t.id)
		}
	}
	if err != nil {
		t.sqlError = "新会话创建失败：" + err.Error()
		s.status = t.sqlError
		return
	}
	t.sqlLease = lease
	t.sqlProfileID = id
	t.sqlDatabase = t.sqlRecoverySchema
	t.sqlRecoveryNeeded = false
	t.sqlRecoveryProfile = 0
	t.sqlRecoverySchema = ""
	t.sqlResult = nil
	t.sqlGrid = ui.ListState{}
	t.sqlError = ""
	s.sessions.Activate(id)
	s.selectedProfileID = id
	s.status = "已创建新 SQL 会话；未执行任何 SQL，请自行核对事务结果后再运行"
	s.startMySQLSQLPreconnect(t, lease)
}
