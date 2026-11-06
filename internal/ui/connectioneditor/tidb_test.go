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

func TestTiDBEditorRestoresVaultAndRollsBackFailedSave(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.SetCredentialVault(&memoryCredentialVault{secrets: map[string]string{}})
	if err := s.OpenNew(connections.TiDB, ""); err != nil {
		t.Fatal(err)
	}
	if s.Title() != "新建连接 (TiDB)" || !s.connectionSavePassword {
		t.Fatal("TiDB editor identity or vault defaults")
	}
	s.connectionDraft.Name = "TiDB vault"
	s.connectionSecret = "private-tidb"
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatalf("TiDB save: %v %s", err, s.Error())
	}
	p := profiles[0]
	s.OpenProfile(p)
	if p.Kind != connections.TiDB || s.connectionSecret != "private-tidb" || !s.connectionProfilesVisible {
		t.Fatal("TiDB reopen lost identity, password or configuration sidebar")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
	if !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("TiDB configurations unavailable")
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
		t.Fatal("failed vault save changed original TiDB profile")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	if s.connectionSecret != "private-tidb" {
		t.Fatal("rollback changed saved password")
	}
}

func TestTiDBConnectionProbeCapturesProductAndRedactsSecret(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.TiDB)
	s.nextConnectionStep()
	s.connectionDraft.Name = "TiDB probe"
	s.connectionSecret = "private-probe"
	called := make(chan connections.ActiveSettings, 1)
	s.mysqlConnectionTester = func(_ context.Context, settings connections.ActiveSettings, secret string) error {
		called <- settings
		return errors.New("fixture denied " + secret)
	}
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("测试当前 TiDB 配置"); err != nil {
		t.Fatal(err)
	}
	select {
	case settings := <-called:
		if settings.Kind != connections.TiDB || settings.Port != 4000 {
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
		t.Fatal("TiDB probe failed to settle safely")
	}
}
