package connections_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestServiceDefaultsAndDuplicate(t *testing.T) {
	serviceStore, _, _ := openStore(t)
	s := connections.NewService(serviceStore)
	ctx := context.Background()
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
		def, err := connections.Defaults(kind)
		if err != nil || def.Kind != kind {
			t.Fatalf("defaults(%s): %+v %v", kind, def, err)
		}
		switch kind {
		case connections.MySQL:
			if def.Port != 3306 || def.Host != "localhost" {
				t.Fatalf("mysql default: %+v", def)
			}
		case connections.PostgreSQL:
			if def.Port != 5432 || def.Database != "postgres" {
				t.Fatalf("postgres default: %+v", def)
			}
		}
	}
	if _, err := connections.Defaults("unknown"); !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	first, err := s.Create(ctx, connections.Profile{Name: "Original", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Duplicate(ctx, first.ID, "Original copy")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || second.Name != "Original copy" || second.Host != first.Host {
		t.Fatalf("duplicate: %+v", second)
	}
	if _, err = s.Duplicate(ctx, first.ID, " "); !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("empty duplicate: %v", err)
	}
	if _, err = s.Duplicate(ctx, 999, "Missing"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err = s.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	original, err := s.Get(ctx, second.ID)
	if err != nil || original.Name != "Original copy" {
		t.Fatalf("copy was deleted with source: %+v %v", original, err)
	}
}
