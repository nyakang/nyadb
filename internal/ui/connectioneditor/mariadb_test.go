package connectioneditor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMariaDBEditorRestoresVaultAndRollsBackFailedSave(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.SetCredentialVault(&memoryCredentialVault{secrets: map[string]string{}})
	if err := s.OpenNew(connections.MariaDB, ""); err != nil {
		t.Fatal(err)
	}
	if s.Title() != "新建连接 (MariaDB)" || !s.connectionSavePassword {
		t.Fatal("MariaDB editor identity or vault defaults")
	}
	s.connectionDraft.Name = "MariaDB vault"
	s.connectionSecret = "private-mariadb"
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatalf("MariaDB save: %v %s", err, s.Error())
	}
	p := profiles[0]
	s.OpenProfile(p)
	if p.Kind != connections.MariaDB || s.connectionSecret != "private-mariadb" || !s.connectionProfilesVisible {
		t.Fatal("MariaDB reopen lost identity, password or configuration sidebar")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
	if !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("MariaDB configurations unavailable")
	}
	for _, tab := range []string{"高级", "SSL", "SSH", "HTTP"} {
		if err := tt.Click(tab); err != nil {
			t.Fatal(err)
		}
	}
	vault := s.credentialVault.(*memoryCredentialVault)
	vault.setErr = errors.New("vault locked")
	s.connectionDraft.Host = "changed.example"
	s.connectionSecret = "replacement"
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || s.Error() == "" {
		t.Fatal("failed vault save changed original MariaDB profile")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	if s.connectionSecret != "private-mariadb" {
		t.Fatal("rollback changed saved password")
	}
}

func TestMariaDBConnectionProbeCapturesProductAndRedactsSecret(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MariaDB)
	s.nextConnectionStep()
	s.connectionDraft.Name = "MariaDB probe"
	s.connectionSecret = "private-probe"
	called := make(chan connections.ActiveSettings, 1)
	s.mysqlConnectionTester = func(_ context.Context, settings connections.ActiveSettings, secret string) error {
		called <- settings
		return errors.New("fixture denied " + secret)
	}
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("测试当前 MariaDB 配置"); err != nil {
		t.Fatal(err)
	}
	select {
	case settings := <-called:
		if settings.Kind != connections.MariaDB || settings.Port != 3306 {
			t.Fatal("probe lost product")
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.connectionTesting || strings.Contains(s.connectionError, "private-probe") || !strings.Contains(s.connectionError, "[已隐藏]") {
		t.Fatal("MariaDB probe failed to settle safely")
	}
}
