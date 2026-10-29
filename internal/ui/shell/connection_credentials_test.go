package shell

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type memoryCredentialVault struct {
	secrets map[string]string
	getErr  error
	setErr  error
}

func (v *memoryCredentialVault) key(id int64, variant string) string {
	return fmt.Sprintf("%d/%s", id, variant)
}

func (v *memoryCredentialVault) Get(id int64, variant string) (string, bool, error) {
	if v.getErr != nil {
		return "", false, v.getErr
	}
	p, ok := v.secrets[v.key(id, variant)]
	return p, ok, nil
}

func (v *memoryCredentialVault) Set(id int64, variant, password string) error {
	if v.setErr != nil {
		return v.setErr
	}
	v.secrets[v.key(id, variant)] = password
	return nil
}

func (v *memoryCredentialVault) Delete(id int64, variant string) error {
	delete(v.secrets, v.key(id, variant))
	return nil
}

func editMySQLProfileTester(t *testing.T) (*State, *ui.Tester, *connections.Service, int64) {
	t.Helper()
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Editing", Kind: connections.MySQL, Host: "localhost", Port: 3306, Username: "root",
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.startEditProfile()
	return s, ui.NewTester(s.ConnectionWindowView, 900, 700), service, p.ID
}

func TestMySQLSavedPasswordNewConnectionAndSession(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string)}
	s.SetCredentialVault(vault)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionEditor.Draft().Name = "Saved new"
	passwordEditor := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := passwordEditor.Click("密码"); err != nil {
		t.Fatal(err)
	}
	passwordEditor.Type("from-new-connection")
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatalf("new password was not saved: %s", s.connectionEditor.Error())
	}
	id := s.selectedProfileID
	if password, found, _ := vault.Get(id, ""); !found || password != "from-new-connection" {
		t.Fatal("new profile password missing")
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || p.Name != "Saved new" {
		t.Fatalf("new connection profile missing: %v", err)
	}
	s.sessions.Opener = func(_ context.Context, st connections.ActiveSettings, password string) error {
		if st.ProfileID != id || password != "from-new-connection" {
			return errors.New("wrong stored password")
		}
		return nil
	}
	s.sessions.DatabaseLister = func(context.Context, int64) ([]string, error) { return []string{"app"}, nil }
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.sessions.Current().Status != "已连接" {
		t.Fatalf("stored password not used for session: %s", s.sessions.Current().Error)
	}
}

func TestDeletingMySQLConnectionCleansSavedCredentials(t *testing.T) {
	s, _, _, id := editMySQLProfileTester(t)
	vault := &memoryCredentialVault{secrets: map[string]string{
		fmt.Sprintf("%d/", id):        "secret",
		fmt.Sprintf("%d/Staging", id): "variant",
	}}
	s.SetCredentialVault(vault)
	s.pendingDeleteID = id
	s.deleteProfile()
	if _, found, _ := vault.Get(id, ""); found {
		t.Fatal("deleted profile left its primary credential")
	}
	// The profile had no persisted variant; deleting a connection must remove
	// exactly the credentials it knows rather than unrelated keyring entries.
	if v, found, _ := vault.Get(id, "Staging"); !found || v != "variant" {
		t.Fatal("cleanup unexpectedly removed an unknown vault entry")
	}
}
