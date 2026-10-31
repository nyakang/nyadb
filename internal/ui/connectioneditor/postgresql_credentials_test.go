package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestPostgreSQLCredentialAccountCompatibilityAndFixedDatabase(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: make(map[string]string)}
	s.SetCredentialVault(vault)
	if e := s.OpenNew(connections.PostgreSQL, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "PostgreSQL fixture"
	s.connectionDraft.Database = "fixed_db"
	s.connectionSecret = "private-pg-password"
	s.connectionSavePassword = true
	s.Save()
	profiles, e := service.List(context.Background())
	if e != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatalf("PG save %v %s", e, s.Error())
	}
	p := profiles[0]
	if got, found, e := vault.Get(p.ID, ""); e != nil || !found || got != "private-pg-password" {
		t.Fatalf("existing profile/account key not used %q %v %v", got, found, e)
	}
	if strings.Contains(fmt.Sprint(p), "private-pg-password") {
		t.Fatal("PG password leaked into profile")
	}
	s.OpenProfile(p)
	if s.connectionDraft.ID != p.ID || s.connectionDraft.Kind != connections.PostgreSQL {
		t.Fatal("PG vault restore discarded immutable profile identity")
	}
	if !s.connectionSavePassword || s.connectionSecret != "private-pg-password" {
		t.Fatal("PG stored password not restored into masked draft")
	}
	s.connectionDraft.Database = ""
	s.Save()
	if s.Error() == "" {
		t.Fatal("PostgreSQL profile was allowed without a fixed database")
	}
	saved, e := service.Get(context.Background(), p.ID)
	if e != nil || saved.Database != "fixed_db" {
		t.Fatal("invalid edit changed saved database")
	}
	s.OpenProfile(p)
	s.connectionSavePassword = false
	s.Save()
	if s.Error() != "" {
		t.Fatal(s.Error())
	}
	if _, found, _ := vault.Get(p.ID, ""); found {
		t.Fatal("explicit unchecked save did not remove old PG password")
	}
}
func TestPostgreSQLVaultWriteFailureRestoresProfileAndOldCredential(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, e := service.Create(context.Background(), connections.Profile{Name: "PG existing", Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: 5432, Database: "fixed_db"})
	if e != nil {
		t.Fatal(e)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "old"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.Host = "changed.invalid"
	s.connectionSecret = "new"
	vault.setErr = errors.New("locked vault")
	s.Save()
	if s.Error() == "" {
		t.Fatal("vault failure not reported")
	}
	saved, e := service.Get(context.Background(), p.ID)
	if e != nil || saved.Host != p.Host {
		t.Fatalf("profile not rolled back %+v %v", saved, e)
	}
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "old" {
		t.Fatal("old credential overwritten")
	}
}
