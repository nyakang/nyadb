package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestClickHouseProfileRestoresWithoutSchemaMigrationOrForeignEngineOptions(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.ClickHouse)
	p.Name = "native persisted"
	p.TLSMode = "verify-full"
	p.Port = 9440
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
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
	if err != nil || got.Kind != connections.ClickHouse || got.TLSMode != saved.TLSMode || got.Port != 9440 || got.Database != "default" {
		t.Fatal("native profile restart failed")
	}
	var after int
	if err = reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected native profile migration")
	}
	for _, raw := range []string{`{"version":2,"kind":"clickhouse"}`, `{"version":1,"kind":"clickhouse","sqlite":{}}`, `{"version":1,"kind":"clickhouse","password":"private"}`} {
		if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Get(ctx, saved.ID); err == nil {
			t.Fatal("corrupt or foreign engine options accepted")
		}
	}
}
