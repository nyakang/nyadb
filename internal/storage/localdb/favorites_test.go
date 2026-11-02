package localdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/favorites"
)

func TestFavoritesRestartIsolationAndConditionalPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewFavoritesRepository(db)
	store := favorites.NewStore(repo)
	empty, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	draft := empty.Clone()
	draft.Groups = []favorites.Group{{ID: "g", Name: " Group "}}
	draft.Entries = []favorites.Entry{{ID: "a", ProfileID: 7, Kind: "object", Name: " Items ", Object: contract.NamespaceRef{ProfileID: 7, Catalog: "main", Schema: "main"}.Object("table", "items"), NamespaceLabel: "main", GroupID: "g"}, {ID: "b", ProfileID: 8, Kind: "query", QueryID: "query-b", Name: "Items"}}
	saved, err := store.Save(ctx, empty, draft)
	if err != nil || saved.Groups[0].Name != "Group" || saved.Entries[0].Name != "Items" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	loaded, err := favorites.NewStore(NewFavoritesRepository(second)).Load(ctx)
	if err != nil || loaded.Revision != 1 || len(loaded.Entries) != 2 || loaded.Entries[0].ProfileID == loaded.Entries[1].ProfileID {
		t.Fatal("restart lost qualified references")
	}
	renamed := loaded.Clone()
	renamed.Entries[0].Name = "Renamed"
	if _, err = store.Save(ctx, loaded, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, saved, saved); !errors.Is(err, favorites.ErrConflict) {
		t.Fatalf("stale snapshot overwrote record: %v", err)
	}
	if _, err = store.Save(ctx, empty, draft); !errors.Is(err, favorites.ErrConflict) {
		t.Fatal("empty old snapshot overwrote existing record")
	}
	// Deleted directories cannot be silently resurrected by a stale writer.
	if _, err = db.Exec("DELETE FROM app_preferences WHERE key = ?", favorites.PreferenceKey); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, loaded, renamed); !errors.Is(err, favorites.ErrConflict) {
		t.Fatal("late publication resurrected removed directory")
	}
}

func TestFavoritesInvalidFutureAndDuplicateGroupsPreserveOriginal(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewFavoritesRepository(db)
	store := favorites.NewStore(repo)
	for _, raw := range []string{`{"version":2,"revision":1}`, `{"version":1,"revision":1,"future":"unknown"}`, `{"version":1} {"version":1}`, `invalid`} {
		if err = SetPreference(db, favorites.PreferenceKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Load(ctx); !errors.Is(err, favorites.ErrInvalid) {
			t.Fatal("future or malformed record accepted")
		}
		after, _ := repo.Read(ctx)
		if after != raw {
			t.Fatal("read rewrote original directory")
		}
	}
	if err = SetPreference(db, favorites.PreferenceKey, ""); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, draft := range []favorites.Snapshot{
		{Groups: []favorites.Group{{ID: "g", Name: "same"}, {ID: "h", Name: "same"}}},
		{Entries: []favorites.Entry{{ID: "a", ProfileID: 7, Kind: "query", QueryID: "q", Name: "name", GroupID: "missing"}}},
		{Entries: []favorites.Entry{{ID: "a", ProfileID: 7, Kind: "query", QueryID: "q", Name: "name"}, {ID: "b", ProfileID: 7, Kind: "query", QueryID: "q", Name: "alias"}}},
		{Entries: []favorites.Entry{{ID: "a", ProfileID: 7, Kind: "object", Name: "name", Object: contract.ObjectRef{ProfileID: 8, Catalog: "db", Kind: "table", Name: "name"}, NamespaceLabel: "db"}}},
	} {
		if _, err = store.Save(ctx, before, draft); !errors.Is(err, favorites.ErrInvalid) {
			t.Fatalf("invalid directory accepted: %v", err)
		}
		raw, _ := repo.Read(ctx)
		if raw != "" {
			t.Fatal("failed save changed original directory")
		}
	}
}
