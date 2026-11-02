package shell

import (
	"context"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLCompletionCatalogCopiesOnlyOriginalProfileMetadata(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	a, err := profiles.Create(context.Background(), connections.Profile{Name: "Original Completion", Kind: connections.SQLite, Database: "/tmp/nyadb-completion-original.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := profiles.Create(context.Background(), connections.Profile{Name: "Other Completion", Kind: connections.SQLite, Database: "/tmp/nyadb-completion-other.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.sessions.Activate(a.ID)
	s.sessions.Current().ProfileID = a.ID
	s.sessions.Current().Cache["main"] = []contract.DBObject{{Name: "original_table", Kind: "表"}, {Name: "program", Kind: "函数"}}
	s.sessions.Activate(b.ID)
	s.sessions.Current().ProfileID = b.ID
	s.sessions.Current().Cache["main"] = []contract.DBObject{{Name: "other_table", Kind: "表"}}
	reads := 0
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]contract.DBObject, error) { reads++; return nil, nil }
	catalog := s.sqlCompletionCatalog(a.ID, "main")
	if len(catalog.Objects) != 1 || catalog.Objects[0].Name != "original_table" || catalog.Dialect != "sqlite" || reads != 0 || !slices.Contains(catalog.Namespaces, "main") {
		t.Fatalf("completion escaped original offline cache: %+v reads=%d", catalog, reads)
	}
	catalog.Objects[0].Name = "changed"
	if s.sessions.CacheFor(a.ID)["main"][0].Name != "original_table" {
		t.Fatal("completion snapshot aliased live navigation metadata")
	}
	if unknown := s.sqlCompletionCatalog(999, "main"); len(unknown.Objects) != 0 {
		t.Fatal("unknown profile inherited active metadata")
	}
}
