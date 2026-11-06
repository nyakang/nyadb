package localdb

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestOpenGaussRestartPreservesProductCertificatesAndLegacyPostgres(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name = "openGauss persisted"
	p.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	p.PostgreSQLOptions.DefaultSchema = "app"
	p.PostgreSQLOptions.SQLMaxConnections = 3
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
	if err != nil || got.Kind != connections.OpenGauss || got.PostgreSQLOptions != saved.PostgreSQLOptions {
		t.Fatal("restart lost product/options", err, got)
	}
	previous, err := store.Get(ctx, old.ID)
	if err != nil || previous.Kind != connections.PostgreSQL || previous.PostgreSQLOptions != connections.DefaultPostgreSQLOptions() {
		t.Fatal("legacy profile changed", err)
	}
}
