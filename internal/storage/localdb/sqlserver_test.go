package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLServerProfileRestartDuplicateAndEnvelopeValidation(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.SQLServer)
	p.Name = "persisted SQL Server"
	p.Database = "tenant"
	p.SQLServerOptions.ApplicationName = "restored"
	p.SQLServerOptions.MaxConnections = 3
	p.SQLServerOptions.SQLMaxConnections = 5
	p.SQLServerOptions.CAFile = "/fixture/ca.pem"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := connections.NewService(store).Duplicate(ctx, saved.ID, "copied")
	if err != nil || duplicate.SQLServerOptions != saved.SQLServerOptions || duplicate.Kind != connections.SQLServer {
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
	if err != nil || got.SQLServerOptions != saved.SQLServerOptions || got.Database != "tenant" || got.TLSMode != "verify-full" {
		t.Fatal("restart", err)
	}
	for _, raw := range []string{`{"version":1,"kind":"sqlserver","sqlserver":{"version":2}}`, `{"version":1,"kind":"sqlserver","sqlserver":{"password":"secret"}}`, `{"version":1,"kind":"sqlserver","postgresql":{}}`} {
		if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Get(ctx, saved.ID); err == nil {
			t.Fatal("corrupt envelope accepted")
		}
	}
}
