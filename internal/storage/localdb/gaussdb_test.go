package localdb

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestGaussDBRestartPreservesProductCertificatesAndLegacyPostgres(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.GaussDB)
	p.Name = "GaussDB persisted"
	p.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	p.PostgreSQLOptions.DefaultSchema = "app"
	p.PostgreSQLOptions.SQLMaxConnections = 3
	p.GaussDBOptions.AdditionalEndpoints = "replica:5433,[::1]:5434"
	p.GaussDBOptions.TargetSessionAttrs = "primary"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := connections.Defaults(connections.PostgreSQL)
	legacy.Name = "legacy"
	old, err := store.Create(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", old.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.Kind != connections.GaussDB || got.PostgreSQLOptions != saved.PostgreSQLOptions || got.GaussDBOptions != saved.GaussDBOptions {
		t.Fatal("restart lost product/options", err, got)
	}
	if err = decodeEngineOptions(&got, `{"version":1,"kind":"gaussdb","gaussdb":{"version":1},"postgresql":{},"redis":{}}`); err == nil {
		t.Fatal("foreign saved options accepted")
	}
	previous, err := store.Get(ctx, old.ID)
	if err != nil || previous.Kind != connections.PostgreSQL || previous.PostgreSQLOptions != connections.DefaultPostgreSQLOptions() {
		t.Fatal("legacy profile changed", err)
	}
}
