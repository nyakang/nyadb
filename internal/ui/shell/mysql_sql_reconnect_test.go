package shell

import (
	"context"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// A profile reconnect may create a new authenticated MySQL session, but it
// cannot resume a disconnected editor's old transaction or temporary state.
func TestDisconnectedSQLTabNeverRebindsAfterProfileReconnect(t *testing.T) {
	s, tt, profileID := sqlUITestState(t)
	old := s.currentTab()
	old.query.Editor.Buffer.Set("START TRANSACTION;\nUPDATE tx_probe SET note='pending';")
	s.sqlWorkspace.DisconnectProfile(profileID)
	if old.query.ProfileID() != 0 || old.query.Database() != "" || old.query.Result() != nil {
		t.Fatalf("disconnected tab retained its old SQL session: profile=%d database=%q", old.query.ProfileID(), old.query.Database())
	}
	if !strings.Contains(old.query.Error(), "事务") || old.query.Editor.Buffer.String() != "START TRANSACTION;\nUPDATE tx_probe SET note='pending';" {
		t.Fatalf("disconnected tab lost its text or did not warn: %q", old.query.Error())
	}

	// The profile becomes connected again; the old tab must not run against
	// the new session even though its text is still editable.
	s.sessions.Current().ProfileID = profileID
	s.sessions.Current().Status = "已连接"
	calls := 0
	s.sqlWorkspace.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		calls++
		return mysqlsvc.MySQLSQLResult{}, nil
	}
	s.sqlWorkspace.Run(nil, old.query)
	if calls != 0 || old.query.Loading() || !strings.Contains(old.query.Error(), "以新会话继续") {
		t.Fatalf("old tab ran after reconnect: calls=%d err=%q", calls, old.query.Error())
	}
	s.openQuery()
	tt.Frame()
	newTab := s.currentTab()
	if newTab == old || newTab.query.ProfileID() != profileID {
		t.Fatalf("new tab did not bind to reconnected profile: %d", newTab.query.ProfileID())
	}
}
