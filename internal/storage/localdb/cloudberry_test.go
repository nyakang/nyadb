package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCloudberryRestartPreservesProductAndPostgresOptions(t *testing.T) {
	store, path, db := openStore(t)
	p, _ := connections.Defaults(connections.Cloudberry)
	p.Name, p.Host, p.Database = "Cloudberry", "warehouse.example", "analytics"
	p.TLSMode = "verify-full"
	p.PostgreSQLOptions.DefaultSchema, p.PostgreSQLOptions.CAFile = "tenant", "/fixture/cloudberry-ca.pem"
	p.PostgreSQLOptions.MaxConnections, p.PostgreSQLOptions.SQLMaxConnections = 7, 3
	original, err := store.Create(context.Background(), p)
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
	got, err := NewProfileStore(reopened).Get(context.Background(), original.ID)
	if err != nil || got.Kind != connections.Cloudberry || got.Host != p.Host || got.Database != p.Database || got.PostgreSQLOptions != p.PostgreSQLOptions {
		t.Fatal("restart changed concrete product or native options", err)
	}
}
