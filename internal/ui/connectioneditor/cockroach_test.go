package connectioneditor

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestCockroachEditorPersistsPublicSettingsAndVaultRollback(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.CockroachDB, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "CockroachDB vault"
	s.connectionSavePassword = true
	s.connectionSecret = "private-cockroach"
	s.connectionDraft.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("profile save", err, s.Error())
	}
	p := profiles[0]
	s.OpenProfile(p)
	if s.Title() != "编辑连接 (CockroachDB)" || s.connectionSecret != "private-cockroach" || s.connectionDraft.Port != 26257 {
		t.Fatal("reopen lost product or vault secret", s.Title(), s.connectionSecret, s.connectionDraft.Port, s.connectionSavePassword, vault.secrets)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
	if err = tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("备份工具目录：") {
		t.Fatal("PostgreSQL tools shown for CockroachDB")
	}
	if err = tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"CockroachDB TLS 模式", "CockroachDB CA 证书", "CockroachDB 客户端证书", "CockroachDB 客户端密钥"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("missing product-specific TLS input", label)
		}
	}
	vault.setErr = errors.New("locked")
	s.connectionDraft.Host = "changed.example"
	s.connectionSecret = "replacement"
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || s.Error() == "" {
		t.Fatal("vault failure changed profile", err)
	}
	vault.setErr = nil
	s.OpenProfile(p)
	if s.connectionSecret != "private-cockroach" {
		t.Fatal("vault rollback lost original secret")
	}
}
