package connectioneditor

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"os"
	"strings"
	"testing"
)

type tunnelEditorVault struct {
	values map[string]string
	fail   bool
}

func (v *tunnelEditorVault) Get(_ int64, name string) (string, bool, error) {
	if v.fail {
		return "", false, errors.New("locked")
	}
	value, ok := v.values[name]
	return value, ok, nil
}
func (v *tunnelEditorVault) Set(_ int64, name, value string) error {
	if v.fail {
		return errors.New("locked")
	}
	if v.values == nil {
		v.values = map[string]string{}
	}
	v.values[name] = value
	return nil
}
func (v *tunnelEditorVault) Delete(_ int64, name string) error {
	if v.fail {
		return errors.New("locked")
	}
	delete(v.values, name)
	return nil
}
func TestTunnelEditorSaveMemoryRestoreAndVariantSecretsRemainSeparate(t *testing.T) {
	s, _, service, path := profileTestState(t)
	store := credentials.NewTunnelStore(false)
	s.SetTunnelServices(store, nil, nil)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "memory SSH"
	s.connectionDraft.MySQLAdvanced.SSH.Enabled = true
	s.connectionDraft.MySQLAdvanced.SSH.Host = "fixture"
	s.connectionDraft.MySQLAdvanced.SSH.Username = "fixture"
	s.mysqlSSHPassword = "only-memory"
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatal(s.connectionError)
	}
	profiles, e := service.List(context.Background())
	if e != nil || len(profiles) != 1 {
		t.Fatal(e)
	}
	got, stored, e := store.Get(profiles[0].ID, "")
	if e != nil || stored || got.SSHPassword != "only-memory" {
		t.Fatal("memory secret unavailable after saving profile")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "only-memory") {
		t.Fatal("secret in SQLite")
	}
	s.startEditProfile()
	if s.mysqlSSHPassword != "only-memory" {
		t.Fatal("process secret not restored to editor")
	}
	s.addConnectionConfiguration()
	s.mysqlSSHPassword = "alternate-memory"
	s.captureConnectionConfiguration()
	s.selectConnectionConfiguration(0)
	if s.mysqlSSHPassword != "only-memory" {
		t.Fatal("alternate secret leaked to primary")
	}
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatal(s.connectionError)
	}
	p, _ := service.Get(context.Background(), profiles[0].ID)
	if len(p.Variants) != 1 {
		t.Fatal("variant missing")
	}
	alt, _, e := store.Get(p.ID, p.Variants[0].Name)
	if e != nil || alt.SSHPassword != "alternate-memory" {
		t.Fatal("alternate secret missing")
	}
	s.ConnectionWindowClosed()
	if s.mysqlSSHPassword != "" {
		t.Fatal("close retained transient password")
	}
}
func TestTunnelEditorKeyringFailureRestoresProfileAndDatabasePassword(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	dbVault := &tunnelEditorVault{}
	nativeTunnel := &tunnelEditorVault{}
	s.SetCredentialVault(dbVault)
	store := credentials.NewTunnelStoreWithVault(nativeTunnel)
	s.SetTunnelServices(store, nil, nil)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "saved"
	s.connectionSecret = "old-db"
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatal(s.connectionError)
	}
	id := s.selectedProfileID
	s.startEditProfile()
	s.connectionDraft.Name = "changed"
	s.connectionDraft.MySQLAdvanced.SSH.Enabled = true
	s.connectionDraft.MySQLAdvanced.SSH.Host = "fixture"
	s.connectionDraft.MySQLAdvanced.SSH.Username = "fixture"
	s.connectionSecret = "new-db"
	s.mysqlSSHPassword = "new-tunnel"
	s.mysqlSaveTunnelSecrets = true
	nativeTunnel.fail = true
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("keyring failure hidden")
	}
	p, e := service.Get(context.Background(), id)
	if e != nil || p.Name != "saved" {
		t.Fatalf("profile rollback: %+v %v", p, e)
	}
	secret, _, e := dbVault.Get(id, "")
	if e != nil || secret != "old-db" {
		t.Fatal("database secret not restored after tunnel error")
	}
	if s.mysqlSSHPassword != "new-tunnel" {
		t.Fatal("failed save lost input")
	}
	if _, _, e = store.Get(id, ""); e == nil {
		t.Fatal("unreadable old tunnel record falsely absent")
	}
}
