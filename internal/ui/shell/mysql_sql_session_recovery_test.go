package shell

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLSessionLostOffersOptInRecoveryAndDoesNotExecute(t *testing.T) {
	s, tt, _ := sqlUITestState(t)
	q := s.currentTab()
	const sqlText = "UPDATE tx_probe SET note='do_not_replay'"
	q.editor.Buffer.Set(sqlText)
	calls := 0
	s.mysqlSQLExecutor = func(context.Context, int64, string, string) (connections.MySQLSQLResult, error) {
		calls++
		return connections.MySQLSQLResult{}, connections.ErrMySQLSQLSessionLost
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if calls != 1 || !q.sqlRecoveryNeeded || !strings.Contains(q.sqlError, "事务") {
		t.Fatalf("session loss did not show recovery warning: calls=%d state=%v msg=%q", calls, q.sqlRecoveryNeeded, q.sqlError)
	}
	tt.Frame()
	// A stale action cannot re-run the old SQL, even with the mock executor.
	s.runMySQLSQL(ui.Services{}, q)
	if calls != 1 || q.sqlLoading || q.editor.Buffer.String() != sqlText {
		t.Fatal("lost SQL tab replayed or dropped its editor content")
	}
	// No session manager is present; clicking a disabled recovery control
	// must not claim recovery or call the SQL executor.
	_ = tt.Click("建立新物理会话（不执行现有 SQL）")
	tt.Frame()
	if !q.sqlRecoveryNeeded || calls != 1 {
		t.Fatal("unavailable recovery button executed SQL or removed warning")
	}
}

func TestSQLRecoverSameTabRealMySQLIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TAB_IT") != "1" {
		t.Skip("requires explicitly enabled disposable MySQL loopback fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_TAB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("missing valid loopback fixture port")
	}
	s, tt, id := sqlUITestState(t)
	m := connections.NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	s.mysqlSessionManager = m
	cfg := connections.ActiveSettings{
		ProfileID: id, Host: "127.0.0.1", Port: uint16(port),
		Username: "nyadb_tab", MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	}
	ctx := context.Background()
	if err := m.Connect(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	q := s.currentTab()
	const sqlText = "UPDATE nyadb_tab_it.tx_probe SET note='auto_replay_bug' WHERE id=1"
	q.editor.Buffer.Set(sqlText)
	old, err := m.ReserveSQLTabToken(id, q.id)
	if err != nil {
		t.Fatal(err)
	}
	q.sqlLease = old
	// Simulate a confirmed physical session loss, not a new SQL execution.
	q.sqlRecoveryNeeded = true
	q.sqlRecoveryProfile = id
	q.sqlRecoverySchema = "nyadb_tab_it"
	q.sqlError = "会话失效；状态不可恢复"
	tt.Frame()
	if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
		t.Fatal(err)
	}
	if q.sqlRecoveryNeeded || q.sqlLease.IsZero() || q.editor.Buffer.String() != sqlText ||
		q.sqlResult != nil || q.sqlLoading {
		t.Fatalf("recovery lost editor or executed SQL: lost=%v sql=%q", q.sqlRecoveryNeeded, q.editor.Buffer.String())
	}
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT 1"); !errors.Is(err, connections.ErrMySQLSQLSessionLost) {
		t.Fatalf("old token still accepted after recovery: %v", err)
	}
	read, err := m.ExecuteSQLTabToken(ctx, q.sqlLease, "nyadb_tab_it",
		"SELECT note FROM tx_probe WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Rows) != 1 || read.Rows[0][0] != "before" {
		t.Fatalf("recovery automatically replayed DML: %+v", read.Rows)
	}
	// A normal explicit run after recovery is still allowed.
	q.editor.Buffer.Set("SELECT note FROM tx_probe WHERE id=1")
	tt.Frame()
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.sqlResult == nil || len(q.sqlResult.Rows) == 0 || q.sqlResult.Rows[0][0] != "before" {
		t.Fatalf("recovered tab did not execute user-selected SQL: %+v %s", q.sqlResult, q.sqlError)
	}
	// A disconnected profile must be reconnected explicitly, then the
	// preserved editor can recover its original identity in the same tab.
	q.editor.Buffer.Set(sqlText)
	s.cancelMySQLSQLForProfile(id)
	if err := m.Disconnect(id); err != nil {
		t.Fatal(err)
	}
	s.mysqlSessionStatus = "未连接"
	tt.Frame()
	if !q.sqlRecoveryNeeded || q.sqlProfileID != 0 || q.sqlRecoveryProfile != id ||
		q.editor.Buffer.String() != sqlText {
		t.Fatal("explicit disconnect discarded recovery context or editor")
	}
	_ = tt.Click("建立新物理会话（不执行现有 SQL）")
	if !q.sqlRecoveryNeeded {
		t.Fatal("disconnected profile was recovered without a new Connect")
	}
	if err := m.Connect(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	s.mysqlSessionStatus = "已连接"
	tt.Frame()
	if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
		t.Fatal(err)
	}
	if q.sqlRecoveryNeeded || q.sqlProfileID != id || q.sqlDatabase != "nyadb_tab_it" {
		t.Fatal("reconnected profile did not recover original SQL tab")
	}
	verify, err := m.ExecuteSQLTabToken(ctx, q.sqlLease, "nyadb_tab_it", "SELECT note FROM tx_probe WHERE id=1")
	if err != nil || len(verify.Rows) != 1 || verify.Rows[0][0] != "before" {
		t.Fatalf("explicit reconnect replayed SQL or lost the fixture: %+v %v", verify.Rows, err)
	}
}
