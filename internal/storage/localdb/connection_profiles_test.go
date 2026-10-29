package localdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func openStore(t *testing.T) (*ProfileStore, string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nyadb", "config.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewProfileStore(db), path, db
}

func TestProfileCRUDAndReopen(t *testing.T) {
	s, path, _ := openStore(t)
	ctx := context.Background()
	mysql, err := s.Create(ctx, connections.Profile{
		Name: "Production", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		Database: "app", Username: "reader", TLSMode: "verify-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mysql.ID <= 0 || mysql.CreatedAt.IsZero() || mysql.UpdatedAt.IsZero() {
		t.Fatalf("unexpected created profile: %+v", mysql)
	}
	sqlite, err := s.Create(ctx, connections.Profile{
		Name: "Local", Kind: connections.SQLite, Database: "/tmp/local.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, mysql.ID)
	if err != nil || !reflect.DeepEqual(got, mysql) {
		t.Fatalf("read profile: %+v, err=%v", got, err)
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 2 || all[0].ID != sqlite.ID || all[1].ID != mysql.ID {
		t.Fatalf("sorted list: %+v err=%v", all, err)
	}
	mysql.Name = "Updated"
	mysql.Port = 3307
	updated, err := s.Update(ctx, mysql)
	if err != nil || updated.Name != "Updated" || updated.Port != 3307 || updated.CreatedAt != mysql.CreatedAt {
		t.Fatalf("updated profile: %+v err=%v", updated, err)
	}
	if err := s.Delete(ctx, sqlite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, sqlite.ID); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("deleted profile unexpectedly available: %v", err)
	}
	if err := s.Delete(ctx, sqlite.ID); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("missing delete should report ErrNotFound: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened := NewProfileStore(db)
	result, err := reopened.List(ctx)
	if err != nil || len(result) != 1 || result[0].Name != "Updated" {
		t.Fatalf("profiles not persisted: %+v err=%v", result, err)
	}
}

func TestRejectInvalidProfiles(t *testing.T) {
	s, _, _ := openStore(t)
	ctx := context.Background()
	cases := []connections.Profile{
		{Name: "", Kind: connections.SQLite, Database: "a.db"},
		{Name: "invalid", Kind: "mongo", Database: "a.db"},
		{Name: "mysql no port", Kind: connections.MySQL, Host: "localhost"},
		{Name: "sqlite server", Kind: connections.SQLite, Host: "localhost", Database: "a.db"},
		{Name: "sqlite missing file", Kind: connections.SQLite},
		{Name: "mysql bad tls", Kind: connections.MySQL, Host: "localhost", Port: 3306, TLSMode: "arbitrary"},
	}
	for _, p := range cases {
		if _, err := s.Create(ctx, p); !errors.Is(err, connections.ErrInvalid) {
			t.Errorf("expected ErrInvalid for %+v, got %v", p, err)
		}
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 0 {
		t.Fatalf("invalid profiles stored: %+v err=%v", all, err)
	}
}

func TestSQLValuesAreBound(t *testing.T) {
	s, _, _ := openStore(t)
	ctx := context.Background()
	name := "test'); DELETE FROM connection_profiles; --"
	p, err := s.Create(ctx, connections.Profile{Name: name, Kind: connections.PostgreSQL, Host: "db", Port: 5432})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil || got.Name != name {
		t.Fatalf("profile name was modified or SQL executed: %+v err=%v", got, err)
	}
	if _, err := s.Update(ctx, connections.Profile{ID: 987654, Name: "unused", Kind: connections.SQLite, Database: "test.db"}); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("missing update should report ErrNotFound: %v", err)
	}
}

func TestProfilesWithStaleActiveVariantRemainReadable(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, err := store.Create(ctx, connections.Profile{
		Name: "Stale selection", Kind: connections.MySQL, Host: "localhost", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE connection_profiles SET active_variant=? WHERE id=?", "Missing", p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, p.ID)
	if err != nil || got.ActiveVariant != "Missing" {
		t.Fatalf("stale selection prevented profile read: %+v, %v", got, err)
	}
	profiles, err := store.List(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].ActiveVariant != "Missing" {
		t.Fatalf("stale selection prevented profile listing: %+v, %v", profiles, err)
	}
	if _, err := connections.ResolveActiveSettings(got); !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("stale selection resolved to a connection: %v", err)
	}
}
