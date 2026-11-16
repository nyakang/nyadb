package localdb

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestTrinoManagedPinRestartsInOriginalEnvelopeAndLegacyRemainsHTTP(t *testing.T) {
	store, path, db := openStore(t)
	p, _ := connections.Defaults(connections.Trino)
	p.Name = "original managed pin"
	p.Database = "memory"
	a := &p.TrinoOptions
	a.Mode, a.DriverVersion, a.DriverSHA256, a.DriverDirectory = "jdbc", "jdbc-471", strings.Repeat("b", 64), t.TempDir()
	a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections, a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.PoolWaitTimeoutMilliseconds = 4, 0, 4, 120, 30, 1500
	saved, err := store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
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
	got, err := store.Get(context.Background(), saved.ID)
	if err != nil || got.TrinoOptions != *a {
		t.Fatal("restart lost original immutable package or capacity", err)
	}
	legacy := `{"version":1,"kind":"trino","trino":{"version":1,"default_schema":"default"}}`
	if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", legacy, saved.ID); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(context.Background(), saved.ID)
	if err != nil || got.TrinoOptions.Mode != "http" || got.TrinoOptions.DefaultSchema != "default" || got.TrinoOptions.DriverSHA256 != "" {
		t.Fatal("legacy envelope silently migrated to JDBC", err)
	}
}
