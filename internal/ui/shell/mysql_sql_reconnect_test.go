package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// A profile reconnect may create a new authenticated MySQL session, but it
// cannot resume a disconnected editor's old transaction or temporary state.
func TestDisconnectedSQLTabNeverRebindsAfterProfileReconnect(t *testing.T) {
	s, tt, profileID := sqlUITestState(t)
	old := s.currentTab()
	old.editor.Buffer.Set("START TRANSACTION;\nUPDATE tx_probe SET note='pending';")
	s.cancelMySQLSQLForProfile(profileID)
	if old.sqlProfileID != 0 || old.sqlDatabase != "" || old.sqlResult != nil {
		t.Fatalf("disconnected tab retained its old SQL session: profile=%d database=%q", old.sqlProfileID, old.sqlDatabase)
	}
	if !strings.Contains(old.sqlError, "事务") || old.editor.Buffer.String() != "START TRANSACTION;\nUPDATE tx_probe SET note='pending';" {
		t.Fatalf("disconnected tab lost its text or did not warn: %q", old.sqlError)
	}

	// The profile becomes connected again; the old tab must not run against
	// the new session even though its text is still editable.
	s.mysqlSessionProfileID = profileID
	s.mysqlSessionStatus = "已连接"
	calls := 0
	s.mysqlSQLExecutor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		calls++
		return mysqlsvc.MySQLSQLResult{}, nil
	}
	s.runMySQLSQL(ui.Services{}, old)
	if calls != 0 || old.sqlLoading || !strings.Contains(old.sqlError, "以新会话继续") {
		t.Fatalf("old tab ran after reconnect: calls=%d err=%q", calls, old.sqlError)
	}
	s.openQuery()
	tt.Frame()
	newTab := s.currentTab()
	if newTab == old || newTab.sqlProfileID != profileID {
		t.Fatalf("new tab did not bind to reconnected profile: %d", newTab.sqlProfileID)
	}
}
