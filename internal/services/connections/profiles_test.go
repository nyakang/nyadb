package connections

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nyadb", "config.db")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), path
}

func TestProfileCRUDAndReopen(t *testing.T) {
	s, path := openStore(t)
	ctx := context.Background()
	mysql, err := s.Create(ctx, Profile{
		Name: "Production", Kind: MySQL, Host: "localhost", Port: 3306,
		Database: "app", Username: "reader", TLSMode: "verify-full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mysql.ID <= 0 || mysql.CreatedAt.IsZero() || mysql.UpdatedAt.IsZero() {
		t.Fatalf("unexpected created profile: %+v", mysql)
	}
	sqlite, err := s.Create(ctx, Profile{
		Name: "Local", Kind: SQLite, Database: "/tmp/local.db",
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
	if _, err := s.Get(ctx, sqlite.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted profile unexpectedly available: %v", err)
	}
	if err := s.Delete(ctx, sqlite.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete should report ErrNotFound: %v", err)
	}

	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened := NewStore(db)
	result, err := reopened.List(ctx)
	if err != nil || len(result) != 1 || result[0].Name != "Updated" {
		t.Fatalf("profiles not persisted: %+v err=%v", result, err)
	}
}

func TestRejectInvalidProfiles(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	cases := []Profile{
		{Name: "", Kind: SQLite, Database: "a.db"},
		{Name: "invalid", Kind: "mongo", Database: "a.db"},
		{Name: "mysql no port", Kind: MySQL, Host: "localhost"},
		{Name: "sqlite server", Kind: SQLite, Host: "localhost", Database: "a.db"},
		{Name: "sqlite missing file", Kind: SQLite},
		{Name: "mysql bad tls", Kind: MySQL, Host: "localhost", Port: 3306, TLSMode: "arbitrary"},
	}
	for _, p := range cases {
		if _, err := s.Create(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("expected ErrInvalid for %+v, got %v", p, err)
		}
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 0 {
		t.Fatalf("invalid profiles stored: %+v err=%v", all, err)
	}
}

func TestSQLValuesAreBound(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	name := "test'); DELETE FROM connection_profiles; --"
	p, err := s.Create(ctx, Profile{Name: name, Kind: PostgreSQL, Host: "db", Port: 5432})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil || got.Name != name {
		t.Fatalf("profile name was modified or SQL executed: %+v err=%v", got, err)
	}
	if _, err := s.Update(ctx, Profile{ID: 987654, Name: "unused", Kind: SQLite, Database: "test.db"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update should report ErrNotFound: %v", err)
	}
}
