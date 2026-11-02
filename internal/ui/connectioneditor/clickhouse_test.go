package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestClickHouseNativeEditorTLSCredentialsAndRollback(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.ClickHouse, ""); err != nil {
		t.Fatal(err)
	}
	if s.Title() != "新建连接 (ClickHouse)" {
		t.Fatal("native editor title lost its engine identity")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "native fixture")
	if s.connectionDraft.Port != 9000 || s.connectionDraft.Database != "default" {
		t.Fatal("native editor defaults")
	}
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("ClickHouse 使用原生 TCP 协议，默认端口 9000；TLS 服务通常使用 9440。") {
		t.Fatal("native TLS page absent")
	}
	s.connectionDraft.TLSMode = "verify-full"
	s.connectionSecret = "private-native-password"
	s.connectionSavePassword = true
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatalf("native save: %v %s", err, s.Error())
	}
	p := profiles[0]
	if strings.Contains(fmt.Sprint(p), "private-native-password") {
		t.Fatal("password entered public profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "private-native-password" || !s.connectionSavePassword || s.connectionDraft.ID != p.ID {
		t.Fatal("credential restoration changed engine identity")
	}
	s.connectionDraft.Host = "changed.invalid"
	s.connectionSecret = "new"
	vault.setErr = errors.New("locked vault")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || s.Error() == "" {
		t.Fatal("vault failure changed original profile")
	}
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "private-native-password" {
		t.Fatal("vault failure overwrote old secret")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	s.connectionSavePassword = false
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("explicit credential removal failed")
	}
}
