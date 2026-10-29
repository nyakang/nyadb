package shell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNoKeyringStillConnectsWithEmptyMySQLPassword(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Passwordless", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3306,
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	s.SetCredentialVault(&memoryCredentialVault{getErr: errors.New("secret service not running")})
	s.sessions.Opener = func(_ context.Context, _ connections.ActiveSettings, password string) error {
		if password != "" {
			return errors.New("password must remain empty")
		}
		return nil
	}
	s.sessions.DatabaseLister = func(context.Context, int64) ([]string, error) {
		return []string{"app"}, nil
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(2 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(end) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.sessions.Current().Status != "已连接" {
		t.Fatalf("unavailable keyring prevented a passwordless connection: %s", s.sessions.Current().Error)
	}
	s.CloseMySQLSessions()
}
