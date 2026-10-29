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
	s.sessions.Current().Password = "temporary-secret"
	called := 0
	s.sessions.Opener = func(_ context.Context, settings connections.ActiveSettings, password string) error {
		called++
		if password != "temporary-secret" || settings.ProfileID != p.ID {
			return context.Canceled
		}
		return nil
	}
	s.sessions.DatabaseLister = func(_ context.Context, id int64) ([]string, error) {
		if id != p.ID {
			t.Error("incorrect session profile")
		}
		return []string{"information_schema", "app_db"}, nil
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	if (s.sessions.Current().Status != "连接中" && s.sessions.Current().Status != "已连接") || s.sessions.Current().Password != "" {
		t.Fatal("connection not started asynchronously or password retained")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.sessions.Current().Status != "已连接" || called != 1 || len(s.sessions.Current().Databases) != 2 ||
		!strings.Contains(strings.Join(tt.Texts(), "|"), "information_schema") || !strings.Contains(strings.Join(tt.Texts(), "|"), "app_db") {
		t.Fatalf("live database browser incomplete: status=%s names=%v called=%d", s.sessions.Current().Status, s.sessions.Current().Databases, called)
	}
	if err := tt.Click("重连"); err != nil {
		t.Fatal(err)
	}
	if s.sessions.Current().Status != "未连接" || s.sessions.Current().Password != "" {
		t.Fatal("reconnect did not require reentering credentials")
	}
	s.sessions.Current().Password = "temporary-secret"
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if called != 2 || s.sessions.Current().Status != "已连接" {
		t.Fatal("reconnect failed")
	}
	if err := tt.Click("断开连接"); err != nil {
		t.Fatal(err)
	}
	if s.sessions.Current().Status != "未连接" || len(s.sessions.Current().Databases) != 0 ||
		strings.Contains(strings.Join(tt.Texts(), "|"), "app_db") {
		t.Fatal("disconnected workbench displayed stale database names")
	}
	s.CloseMySQLSessions()
}
