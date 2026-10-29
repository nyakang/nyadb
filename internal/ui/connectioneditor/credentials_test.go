package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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

func TestMySQLSavedPasswordReopenCancelAndDelete(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string)}
	s.SetCredentialVault(vault)
	s.closeConnectionDialog()
	s.startEditProfile()
	tt.Frame()
	s.connectionSecret = "db-secret-in-keyring"
	if err := tt.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionSavePassword {
		t.Fatal("Save Password checkbox did not change state")
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("password save failed: %s", s.connectionError)
	}
	if password, found, _ := vault.Get(id, ""); !found || password != "db-secret-in-keyring" {
		t.Fatal("MySQL password not written to vault")
	}
	p, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", p), "db-secret-in-keyring") {
		t.Fatal("credential leaked into connection profile")
	}
	s.startEditProfile()
	if s.connectionSecret != "db-secret-in-keyring" || !s.connectionSavePassword {
		t.Fatal("edit window did not reopen with saved, masked password")
	}
	s.connectionSecret = "discard-me"
	s.closeConnectionDialog()
	s.startEditProfile()
	if s.connectionSecret != "db-secret-in-keyring" {
		t.Fatal("Cancel overwrote a saved password")
	}
	tt.Frame()
	if err := tt.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := vault.Get(id, ""); found {
		t.Fatal("unchecking Save Password failed to delete credential")
	}
	s.startEditProfile()
	if s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("deleted password still visible in edit window")
	}
}

func TestMySQLSavedPasswordVariantsIndependentAndRenamed(t *testing.T) {
	s, _, _, id := editMySQLProfileTester(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string)}
	s.SetCredentialVault(vault)
	s.closeConnectionDialog()
	s.startEditProfile()
	s.connectionSecret = "primary"
	s.connectionSavePassword = true
	s.addConnectionConfiguration()
	s.connectionOtherConfigs[0].name = "Staging"
	s.connectionSecret = "staging"
	s.connectionSavePassword = true
	s.markConnectionConfigurationActive(1)
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatalf("variant password save failed: %s", s.connectionError)
	}
	if p, found, _ := vault.Get(id, ""); !found || p != "primary" {
		t.Fatal("primary password overwritten")
	}
	if p, found, _ := vault.Get(id, "Staging"); !found || p != "staging" {
		t.Fatal("staging password not stored")
	}
	s.startEditProfile()
	if s.connectionActiveConfig != 1 || s.connectionSecret != "staging" {
		t.Fatal("active variant not restored")
	}
	s.connectionOtherConfigs[0].name = "Renamed"
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatalf("rename password save failed: %s", s.connectionError)
	}
	if _, ok, _ := vault.Get(id, "Staging"); ok {
		t.Fatal("old variant credential not removed")
	}
	if p, ok, _ := vault.Get(id, "Renamed"); !ok || p != "staging" {
		t.Fatal("renamed variant lost credential")
	}
}

func TestMySQLPasswordVaultFailureNeverClaimsSuccess(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string), setErr: errors.New("vault locked")}
	s.SetCredentialVault(vault)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "Must not save silently"
	s.connectionSecret = "secret"
	s.connectionSavePassword = true
	s.saveConnectionDraft()
	if !s.connectionDialog || !strings.Contains(s.connectionError, "vault locked") {
		t.Fatalf("missing actionable vault failure: %s", s.connectionError)
	}
	all, err := service.List(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("failed new credential save left unexpected profile: %+v %v", all, err)
	}
}

func TestMySQLPasswordFailureRollsBackExistingProfile(t *testing.T) {
	s, _, service, id := editMySQLProfileTester(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string), setErr: errors.New("vault busy")}
	s.SetCredentialVault(vault)
	s.closeConnectionDialog()
	s.startEditProfile()
	s.connectionDraft.Host = "changed.example.test"
	s.connectionSecret = "should-not-persist"
	s.connectionSavePassword = true
	s.saveConnectionDraft()
	if !s.connectionDialog || !strings.Contains(s.connectionError, "vault busy") {
		t.Fatal("missing saved credential failure")
	}
	got, err := service.Get(context.Background(), id)
	if err != nil || got.Host != "localhost" {
		t.Fatalf("credential failure committed metadata despite rollback: %+v %v", got, err)
	}
	if _, found, _ := vault.Get(id, ""); found {
		t.Fatal("failed save wrote a password")
	}
}

func TestKeyringUnavailableDoesNotBlockOrdinaryProfileEdits(t *testing.T) {
	s, _, service, id := editMySQLProfileTester(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string), getErr: errors.New("locked")}
	s.SetCredentialVault(vault)
	s.closeConnectionDialog()
	s.startEditProfile()
	if !s.connectionPasswordLoadFailed {
		t.Fatal("locked keyring was not detected")
	}
	s.connectionDraft.Host = "new.example.test"
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatalf("ordinary metadata edit unexpectedly blocked by keyring: %s", s.connectionError)
	}
	got, err := service.Get(context.Background(), id)
	if err != nil || got.Host != "new.example.test" {
		t.Fatalf("ordinary metadata edit lost: %+v %v", got, err)
	}
}
