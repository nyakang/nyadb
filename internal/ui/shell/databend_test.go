package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDatabendOfflineCredentialModesCaretFormattingAndDelete(t *testing.T) {
	for _, auth := range []string{"basic", "bearer", "none"} {
		t.Run(auth, func(t *testing.T) {
			s, tt, profiles, _ := profileTestState(t)
			p, _ := connections.Defaults(connections.Databend)
			p.Name = "Original Databend"
			p.DatabendOptions.Auth = auth
			if auth != "basic" {
				p.Username = ""
			}
			p, err := profiles.Create(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			s.SetCredentialVault(vault)
			if err := vault.Set(p.ID, "", "original-secret"); err != nil {
				t.Fatal(err)
			}
			s.loadProfiles()
			s.selectProfileByName(p.Name)
			tt.Frame()
			_, password := tt.Find("本次 Databend 会话密码")
			_, token := tt.Find("本次 Databend 会话令牌")
			if password != (auth == "basic") || token != (auth == "bearer") {
				t.Fatal("public session credential mode differs")
			}
			source := `select 'a\';still one'; select 2;`
			first := `select 'a\';still one'`
			if got := s.sqlStatementAtCaret(p.ID, source, strings.Index(source, "still")); got != first {
				t.Fatal("caret changed Databend escaped literal", got)
			}
			if got := s.sqlStatementAtCaret(p.ID, source, strings.Index(source, "select 2")); got != "select 2" {
				t.Fatal("caret selected a different Databend statement", got)
			}
			formatted, err := s.sqlFormat(p.ID, source)
			if err != nil || !strings.Contains(formatted, `'a\';still one'`) {
				t.Fatal("format changed original Databend literal", formatted, err)
			}
			s.deleteProfileID(p.ID)
			if _, found, err := vault.Get(p.ID, ""); err != nil || found {
				t.Fatal("deleted profile retained credential", err)
			}
		})
	}
}
