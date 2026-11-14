package connectioneditor

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLProductEditorRetainsVaultAndOriginalConfiguration(t *testing.T) {
	for _, kind := range []connections.Kind{connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, service, _ := profileTestState(t)
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			s.SetCredentialVault(vault)
			if err := s.OpenNew(kind, ""); err != nil {
				t.Fatal(err)
			}
			descriptor, _ := connections.Describe(kind)
			if s.Title() != "新建连接 ("+descriptor.Label+")" {
				t.Fatal("editor changed product title")
			}
			s.connectionDraft.Name = descriptor.Label
			s.connectionSecret = "private-product"
			s.Save()
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 || s.Error() != "" {
				t.Fatal("save", err, s.Error())
			}
			original := profiles[0]
			s.OpenProfile(original)
			if s.connectionDraft.Kind != kind || s.connectionSecret != "private-product" || !s.connectionProfilesVisible {
				t.Fatal("reopen lost product, secret or configurations")
			}
			tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
			for _, tab := range []string{"高级", "SSL", "SSH", "HTTP"} {
				if err := tester.Click(tab); err != nil {
					t.Fatal(err)
				}
			}
			vault.setErr = errors.New("vault locked")
			s.connectionSecret = "replacement"
			s.connectionDraft.Host = "changed.example"
			s.Save()
			got, err := service.Get(context.Background(), original.ID)
			if err != nil || got.Host != original.Host || s.Error() == "" {
				t.Fatal("failed vault save changed original profile", err)
			}
			vault.setErr = nil
			s.OpenProfile(original)
			if s.connectionSecret != "private-product" {
				t.Fatal("rollback changed saved credential")
			}
		})
	}
}
