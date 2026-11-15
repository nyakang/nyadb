package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQuestDBOfflineSQLRetainsNativeNamespaceAndDeleteClearsVault(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.QuestDB)
	p.Name = "original QuestDB"
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err = vault.Set(p.ID, "", "original secret"); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.openQuery()
	doc := s.currentTab().query
	if doc == nil || doc.ProfileID() != p.ID || doc.Namespace() != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "qdb"}) || s.sqlCompletionCatalog(p.ID, "qdb").Dialect != "questdb" {
		t.Fatal("offline SQL document lost its native identity")
	}
	s.deleteProfileID(p.ID)
	if _, err := profiles.Get(context.Background(), p.ID); err == nil {
		t.Fatal("original profile was not deleted")
	}
	if _, found, err := vault.Get(p.ID, ""); err != nil || found {
		t.Fatal("deleted profile retained its vault credential", err)
	}
}
