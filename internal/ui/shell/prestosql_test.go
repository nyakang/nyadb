package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPrestoSQLSessionCredentialInputsCaretAndDelete(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.PrestoSQL)
	p.Name = "Original PrestoSQL"
	p.TLSMode = "verify-full"
	p.PrestoSQLOptions.TrustStorePath = t.TempDir() + "/trust.p12"
	p.PrestoSQLOptions.KeyStorePath = t.TempDir() + "/key.p12"
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	encoded, _ := connections.EncodePrestoSQLCredentials(connections.PrestoSQLCredentials{Password: "original", TrustStorePassword: "trust"})
	if err := vault.Set(p.ID, "", encoded); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	tt.Frame()
	for _, label := range []string{"PrestoSQL 会话密码", "PrestoSQL 会话信任库密码", "PrestoSQL 会话密钥库密码", "PrestoSQL 会话访问令牌", "PrestoSQL 会话附加凭据"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("session credential input missing", label)
		}
	}
	source := `select 'a'';still one'; select 2;`
	if got := s.sqlStatementAtCaret(p.ID, source, strings.Index(source, "still")); got != `select 'a'';still one'` {
		t.Fatal("caret changed PrestoSQL quoted literal", got)
	}
	if got := s.sqlStatementAtCaret(p.ID, source, strings.Index(source, "select 2")); got != "select 2" {
		t.Fatal("wrong statement at caret", got)
	}
	formatted, err := s.sqlFormat(p.ID, source)
	if err != nil || !strings.Contains(formatted, `'a'';still one'`) {
		t.Fatal("formatter changed literal", err)
	}
	s.deleteProfileID(p.ID)
	if _, found, err := vault.Get(p.ID, ""); err != nil || found {
		t.Fatal("deleted profile retained credential bundle", err)
	}
}
