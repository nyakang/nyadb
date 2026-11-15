package shell

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2SQLDocumentRetainsOriginalProfileAndDeleteClearsVault(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	p, err := connections.Defaults(connections.H2)
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.Database = "H2 original profile", filepath.Join(t.TempDir(), "owned")
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := vault.Set(p.ID, "", "original secret"); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.openQuery()
	doc := s.currentTab().query
	if doc == nil || doc.ProfileID() != p.ID || doc.Namespace().ProfileID != p.ID || s.sqlPlanEngine(doc.ProfileID()) != "h2" {
		t.Fatal("new H2 SQL document lost its original profile or dialect")
	}
	// An unchanged new SQL document does not block profile deletion.
	s.deleteProfileID(p.ID)
	if _, err := profiles.Get(context.Background(), p.ID); err == nil {
		t.Fatal("original H2 profile was not deleted")
	}
	if _, exists, err := vault.Get(p.ID, ""); err != nil || exists {
		t.Fatal("deleting H2 left its original vault credential", err)
	}
}
