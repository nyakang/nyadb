package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/queries"
)

func TestInitialSQLRepositoryLookupAndScopedDeletion(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLRepository(db)
	store := queries.NewStore(repo, &workspaceTestKey{})
	ctx := context.Background()
	if err := store.SaveInitialSQL(ctx, queries.InitialSQL{ID: "initial", ProfileID: 3, Revision: 1, Text: "SET @v=1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNamed(ctx, queries.Named{ID: "query", Name: "retained", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repo.GetRecord(ctx, "query", "initial"); err != nil || exists {
		t.Fatal("lookup ignored record kind", err)
	}
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "initial"); err != nil || !exists {
		t.Fatal("direct lookup failed", err)
	}
	if err := repo.Delete(ctx, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "initial"); err != nil || !exists {
		t.Fatal("ordinary delete changed initial SQL semantics", err)
	}
	if err := repo.DeleteInitialSQL(ctx, "query"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repo.GetRecord(ctx, "query", "query"); err != nil || !exists {
		t.Fatal("initial delete removed named query", err)
	}
	if err := store.DeleteInitialSQL(ctx, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "initial"); err != nil || exists {
		t.Fatal("initial SQL not deleted", err)
	}
}
