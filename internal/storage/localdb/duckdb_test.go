package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDuckDBProfileRestartDuplicateAndEnvelopeValidation(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.DuckDB)
	p.Name = "persisted DuckDB"
	p.Database = "/fixture/user.duckdb"
	p.DuckDBOptions.ReadOnly = true
	p.DuckDBOptions.MemoryLimitMB = 256
	p.DuckDBOptions.SQLMaxConnections = 3
	p.DuckDBOptions.DefaultSchema = "sales"
	p.DuckDBOptions.LibraryFile = "/fixture/libduckdb.so"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := connections.NewService(store).Duplicate(ctx, saved.ID, "copy")
	if err != nil || duplicate.DuckDBOptions != saved.DuckDBOptions || duplicate.Kind != connections.DuckDB {
		t.Fatal("duplicate", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.DuckDBOptions != saved.DuckDBOptions || got.Database != p.Database {
		t.Fatal("restart", err)
	}
	for _, raw := range []string{`{"version":1,"kind":"duckdb","duckdb":{"version":2}}`, `{"version":1,"kind":"duckdb","duckdb":{"password":"secret"}}`, `{"version":1,"kind":"duckdb","sqlserver":{}}`} {
		if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Get(ctx, saved.ID); err == nil {
			t.Fatal("invalid DuckDB envelope accepted")
		}
	}
}
