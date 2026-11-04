package localdb

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"path/filepath"
	"testing"
)

func TestNamedWorkspaceConditionalUpdatesAndColorMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewSQLRepository(db)
	key := &workspaceTestKey{}
	store := queries.NewStore(repo, key)
	w := queries.NamedWorkspace{ID: "workspace/test", Name: "编辑现场", Revision: 1, Workspace: queries.Workspace{SnapshotVersion: 2, ActiveID: 3, Tabs: []queries.WorkspaceTab{{ID: 3, Kind: 1, Pinned: true}}, Documents: []queries.Document{{ID: 3, Text: "SELECT 'draft'"}}}}
	if err = store.SaveNamedWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	other := queries.NewStore(repo, key)
	list, err := other.ListWorkspaces(ctx)
	if err != nil || len(list) != 1 || !list[0].Workspace.Tabs[0].Pinned {
		t.Fatal("snapshot roundtrip", list, err)
	}
	if err = store.RenameWorkspace(ctx, w.ID, 1, "renamed"); err != nil {
		t.Fatal(err)
	}
	w.Revision = 2
	if err = other.SaveNamedWorkspace(ctx, w); !errors.Is(err, queries.ErrStaleRevision) {
		t.Fatal("stale overwrite accepted", err)
	}
	if err = other.DeleteWorkspace(ctx, w.ID, 2); err != nil {
		t.Fatal(err)
	}
	w.Revision = 3
	if err = store.SaveNamedWorkspace(ctx, w); !errors.Is(err, queries.ErrQueryMissing) {
		t.Fatal("deleted workspace replayed", err)
	}
	p, err := NewProfileStore(db).Create(ctx, connections.Profile{Name: "Color", Kind: connections.MySQL, Host: "localhost", Port: 3306, Color: "#36B37E"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = NewProfileStore(db).Get(ctx, p.ID)
	if err != nil || p.Color != "#36B37E" {
		t.Fatal("color not persistent", err)
	}
}

func TestVersionNineColorMigrationPreservesProfilesAndEncryptedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile, err := NewProfileStore(db).Create(ctx, connections.Profile{Name: "旧连接", Kind: connections.MySQL, Host: "localhost", Port: 3306, Notes: "保留备注", Starred: true})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	key := &workspaceTestKey{}
	store := queries.NewStore(NewSQLRepository(db), key)
	if err = store.SaveNamedWorkspace(ctx, queries.NamedWorkspace{ID: "workspace/retained", Name: "保留加密现场", Revision: 1}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec("ALTER TABLE connection_profiles DROP COLUMN color"); err == nil {
		_, err = db.Exec("PRAGMA user_version = 9")
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := NewProfileStore(db).Get(ctx, profile.ID)
		if readErr != nil || got.Color != "" || got.Name != profile.Name || got.Notes != profile.Notes || !got.Starred {
			db.Close()
			t.Fatal("migration changed the original profile", readErr)
		}
		items, readErr := queries.NewStore(NewSQLRepository(db), key).ListWorkspaces(ctx)
		var version int
		versionErr := db.QueryRow("PRAGMA user_version").Scan(&version)
		db.Close()
		if readErr != nil || len(items) != 1 || items[0].Name != "保留加密现场" || versionErr != nil || version != 10 {
			t.Fatal("migration changed encrypted records or was not idempotent", readErr, versionErr)
		}
	}
}
