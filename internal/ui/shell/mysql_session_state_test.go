package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLWorkbenchConnectBrowseDisconnectAndReconnectPrompt(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Local test", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3306,
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	s.mysqlSessionPassword = "temporary-secret"
	called := 0
	s.mysqlSessionOpener = func(_ context.Context, settings connections.ActiveSettings, password string) error {
		called++
		if password != "temporary-secret" || settings.ProfileID != p.ID {
			return context.Canceled
		}
		return nil
	}
	s.mysqlDatabaseLister = func(_ context.Context, id int64) ([]string, error) {
		if id != p.ID {
			t.Error("incorrect session profile")
		}
		return []string{"information_schema", "app_db"}, nil
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	if (s.mysqlSessionStatus != "连接中" && s.mysqlSessionStatus != "已连接") || s.mysqlSessionPassword != "" {
		t.Fatal("connection not started asynchronously or password retained")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.mysqlSessionStatus == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.mysqlSessionStatus != "已连接" || called != 1 || len(s.mysqlSessionDatabases) != 2 ||
		!strings.Contains(strings.Join(tt.Texts(), "|"), "information_schema") || !strings.Contains(strings.Join(tt.Texts(), "|"), "app_db") {
		t.Fatalf("live database browser incomplete: status=%s names=%v called=%d", s.mysqlSessionStatus, s.mysqlSessionDatabases, called)
	}
	if err := tt.Click("重连"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlSessionStatus != "未连接" || s.mysqlSessionPassword != "" {
		t.Fatal("reconnect did not require reentering credentials")
	}
	s.mysqlSessionPassword = "temporary-secret"
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for s.mysqlSessionStatus == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if called != 2 || s.mysqlSessionStatus != "已连接" {
		t.Fatal("reconnect failed")
	}
	if err := tt.Click("断开连接"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlSessionStatus != "未连接" || len(s.mysqlSessionDatabases) != 0 ||
		strings.Contains(strings.Join(tt.Texts(), "|"), "app_db") {
		t.Fatal("disconnected workbench displayed stale database names")
	}
	s.CloseMySQLSessions()
}

func TestMySQLWorkbenchRequiresTLSForRemoteHost(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "External", Kind: connections.MySQL, Host: "remote.example.test", Port: 3306,
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	called := false
	s.mysqlSessionOpener = func(context.Context, connections.ActiveSettings, string) error { called = true; return nil }
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	if called || s.mysqlSessionStatus != "连接失败" ||
		!strings.Contains(s.mysqlSessionError, "TLS") {
		t.Fatalf("remote plaintext not blocked: status=%s err=%s", s.mysqlSessionStatus, s.mysqlSessionError)
	}
	s.CloseMySQLSessions()
}
