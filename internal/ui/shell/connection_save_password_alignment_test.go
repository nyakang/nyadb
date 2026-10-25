package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// The observed original defaults Save Password to checked on the MySQL
// creation form. This remains opt-out, and never stores an empty secret.
func TestMySQLNewConnectionSavePasswordDefaultAndBack(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if !s.connectionSavePassword || !tt.HasText("保存密码") {
		t.Fatal("new MySQL editor should preselect Save Password with a vault")
	}
	if err := tt.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if s.connectionSavePassword {
		t.Fatal("user cannot opt out of password persistence")
	}
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 || s.connectionSavePassword {
		t.Fatal("Back should clear the save-password draft")
	}
	s.nextConnectionStep()
	tt.Frame()
	if !s.connectionSavePassword {
		t.Fatal("re-entering new MySQL configuration should apply its default")
	}
	s.connectionDraft.Name = "Passwordless"
	// A legitimate passwordless database must not require a vault write.
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatalf("passwordless profile was not saved: %s", s.connectionError)
	}
	if _, found, err := vault.Get(s.selectedProfileID, ""); err != nil || found {
		t.Fatal("empty password created a spurious stored credential")
	}
	p, err := service.Get(context.Background(), s.selectedProfileID)
	if err != nil || p.Name != "Passwordless" {
		t.Fatalf("passwordless profile missing: %+v %v", p, err)
	}
}

func TestSavePasswordDefaultRequiresMySQLAndVault(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	if s.connectionSavePassword {
		t.Fatal("Save Password should not be preselected without a vault")
	}
	s.openConnectionDialog()
	s.SetCredentialVault(&memoryCredentialVault{secrets: map[string]string{}})
	s.chooseConnectionKind(connections.PostgreSQL)
	s.nextConnectionStep()
	if s.connectionSavePassword {
		t.Fatal("MySQL credential default applied to PostgreSQL")
	}
	s.closeConnectionDialog()
	if s.connectionSavePassword {
		t.Fatal("closing the connection editor retained a save-password draft")
	}
}
