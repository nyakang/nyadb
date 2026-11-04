package sqlworkspace

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"testing"
)

func TestWorkspaceRetainsSQLNamespaceAndRebindDoesNotExecute(t *testing.T) {
	s := New(Dependencies{Profiles: func() []ProfileChoice { return []ProfileChoice{{ID: 8, Name: "new"}} }, Namespace: func(id int64, db string) contract.NamespaceRef {
		return contract.NamespaceRef{ProfileID: id, Catalog: "new_catalog", Schema: db}
	}})
	d := s.RestoreDocument(queries.Document{ID: 2, Text: "SELECT '草稿'", ProfileID: 4, Database: "app", Catalog: "original_catalog", Schema: "app"})
	w := s.Snapshot(2, []uint64{2})
	if w.Documents[0].Catalog != "original_catalog" || w.Documents[0].Schema != "app" {
		t.Fatal("original namespace was replaced")
	}
	s.Rebind(d, 8)
	if d.Namespace().Catalog != "new_catalog" || d.RecoveryProfile() != 8 || !d.NeedsRecovery() || d.lease != nil || d.Loading() || d.Editor.Buffer.String() != "SELECT '草稿'" {
		t.Fatal("rebind changed the draft or established a session")
	}
}
