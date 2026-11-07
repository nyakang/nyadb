package connectioneditor

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestGaussDBEditorPersistsPublicSettingsAndVaultRollback(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.GaussDB, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "GaussDB vault"
	s.connectionSavePassword = true
	s.connectionSecret = "private-gaussdb"
	s.connectionDraft.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	s.connectionDraft.GaussDBOptions.AdditionalEndpoints = "replica:5433,[::1]"
	s.connectionDraft.GaussDBOptions.TargetSessionAttrs = "prefer-standby"
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("profile save", err, s.Error())
	}
	p := profiles[0]
	s.OpenProfile(p)
	if s.Title() != "编辑连接 (GaussDB)" || s.connectionSecret != "private-gaussdb" || s.connectionDraft.Port != 5432 || s.connectionDraft.GaussDBOptions.AdditionalEndpoints != "replica:5433,[::1]:5432" || s.connectionDraft.GaussDBOptions.TargetSessionAttrs != "prefer-standby" {
		t.Fatal("reopen lost product or vault secret", s.Title(), s.connectionSecret, s.connectionDraft.Port, s.connectionSavePassword, vault.secrets)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
	if err = tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("备份工具目录：") || tt.HasText("锁等待超时（毫秒）：") {
		t.Fatal("PostgreSQL tools shown for GaussDB")
	}
	for _, label := range []string{"GaussDB 其他端点", "GaussDB 目标会话"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("missing native endpoint control", label)
		}
	}
	if err = tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"GaussDB TLS 模式", "GaussDB CA 证书", "GaussDB 客户端证书", "GaussDB 客户端密钥"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("missing product-specific TLS input", label)
		}
	}
	vault.setErr = errors.New("locked")
	s.connectionDraft.Host = "changed.example"
	s.connectionDraft.GaussDBOptions.AdditionalEndpoints = "changed-replica:5434"
	s.connectionSecret = "replacement"
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || got.GaussDBOptions != p.GaussDBOptions || s.Error() == "" {
		t.Fatal("vault failure changed profile", err)
	}
	vault.setErr = nil
	s.OpenProfile(p)
	if s.connectionSecret != "private-gaussdb" {
		t.Fatal("vault rollback lost original secret")
	}
}
