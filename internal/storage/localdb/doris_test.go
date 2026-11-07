package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDorisProfileRestoresWithoutSchemaMigrationOrForeignEngineOptions(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Doris)
	p.Name = "native persisted"
	p.TLSMode = "verify-full"
	p.Port = 9030
	p.Database = "nyadb"
	p.DorisOptions.CAFile = "/tmp/ca.pem"
	p.DorisOptions.ServerName = "server"
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
	if err != nil || got.Kind != connections.Doris || got.TLSMode != saved.TLSMode || got.Port != 9030 || got.Database != "nyadb" || got.DorisOptions != p.DorisOptions {
		t.Fatal("native profile restart failed")
	}
	var after int
	if err = reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected native profile migration")
	}
	for _, raw := range []string{`{"version":2,"kind":"doris"}`, `{"version":1,"kind":"doris","sqlite":{}}`, `{"version":1,"kind":"doris","password":"private"}`} {
		if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Get(ctx, saved.ID); err == nil {
			t.Fatal("corrupt or foreign engine options accepted")
		}
	}
}
