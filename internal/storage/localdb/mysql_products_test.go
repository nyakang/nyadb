package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLProductRestartPreservesIdentityAndConfiguration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var saved []connections.Profile
	for _, kind := range []connections.Kind{connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt} {
		p, err := connections.Defaults(kind)
		if err != nil {
			t.Fatal(err)
		}
		p.Name = string(kind)
		p.MySQLAdvanced.SSL.Enabled = true
		p.MySQLAdvanced.SSL.CACertificate = "/fixture/ca.pem"
		p.Variants = []connections.ConnectionVariant{{Name: "Replica", Host: "replica.example", Port: 3307, Database: "fixture", Username: "reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
		p.ActiveVariant = "Replica"
		p, err = store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, p)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	for _, original := range saved {
		got, err := store.Get(ctx, original.ID)
		if err != nil || got.Kind != original.Kind || got.MySQLAdvanced != original.MySQLAdvanced || len(got.Variants) != 1 || got.Variants[0] != original.Variants[0] {
			t.Fatal("restart changed original product settings", err)
		}
		active, err := connections.ResolveActiveSettings(got)
		if err != nil || active.Kind != original.Kind || active.Database != "fixture" || active.Host != "replica.example" {
			t.Fatal("restart changed original active target", err)
		}
		got.Host = "updated.example"
		if _, err := store.Update(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
}
