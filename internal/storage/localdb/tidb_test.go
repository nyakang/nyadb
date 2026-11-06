package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTiDBProfileRestartPreservesConfigurationsAndLegacyMySQL(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var schemaBefore int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schemaBefore); err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.TiDB)
	p.Name = "TiDB persisted"
	p.MySQLAdvanced.SSL.Enabled = true
	p.MySQLAdvanced.SSL.CACertificate = "/fixture/ca.pem"
	p.MySQLAdvanced.SSH.Enabled = true
	p.MySQLAdvanced.SSH.Host = "jump.example"
	p.MySQLAdvanced.SSH.Username = "reader"
	p.Variants = []connections.ConnectionVariant{{Name: "Replica", Host: "replica.example", Port: 3307, Username: "reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
	p.ActiveVariant = "Replica"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := connections.Defaults(connections.MySQL)
	legacy.Name = "Legacy MySQL"
	old, err := store.Create(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", old.ID); err != nil {
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
	if err != nil || got.Kind != connections.TiDB || got.MySQLAdvanced != saved.MySQLAdvanced || len(got.Variants) != 1 || got.ActiveVariant != "Replica" {
		t.Fatalf("TiDB restart: %+v %v", got, err)
	}
	settings, err := connections.ResolveActiveSettings(got)
	if err != nil || settings.Kind != connections.TiDB || settings.Host != "replica.example" {
		t.Fatal("restart lost active target")
	}
	got.Host = "updated.example"
	if _, err = store.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	gotLegacy, err := store.Get(ctx, old.ID)
	if err != nil || gotLegacy.Kind != connections.MySQL || gotLegacy.MySQLAdvanced != connections.DefaultMySQLAdvanced() {
		t.Fatal("legacy MySQL changed")
	}
	var schemaAfter int
	if err = reopened.QueryRow("PRAGMA user_version").Scan(&schemaAfter); err != nil || schemaAfter != schemaBefore {
		t.Fatal("TiDB changed existing storage schema")
	}
}
