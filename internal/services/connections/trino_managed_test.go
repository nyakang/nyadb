package connections_test

import (
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrinoManagedOptionsLegacyDefaultsPinAndURI(t *testing.T) {
	legacy, err := connections.NormalizeTrinoOptions(connections.TrinoOptions{Version: 1, DefaultSchema: "default"})
	if err != nil || legacy.Mode != "http" || legacy.MaxConnections != 8 || legacy.MetadataReserve != 2 || legacy.SQLMaxConnections != 6 || legacy.DefaultSchema != "default" {
		t.Fatal("legacy HTTP settings changed", err)
	}
	a := legacy
	a.Mode, a.DriverVersion, a.DriverSHA256, a.DriverDirectory = "jdbc", "jdbc-471", strings.Repeat("a", 64), filepath.Join(t.TempDir(), "drivers")
	a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections = 4, 0, 4
	got, err := connections.NormalizeTrinoOptions(a)
	if err != nil || got != a {
		t.Fatal("original pin or explicit zero metadata reserve changed", err)
	}
	q := url.Values{"mode": {"jdbc"}, "driver_version": {a.DriverVersion}, "driver_sha256": {a.DriverSHA256}, "driver_directory": {a.DriverDirectory}}
	imported, err := connections.ParseURI("trino://user@localhost/memory/default?" + q.Encode())
	if err != nil || imported.Profile.TrinoOptions.Mode != "jdbc" || imported.Profile.TrinoOptions.DriverSHA256 != a.DriverSHA256 || imported.Profile.TrinoOptions.DriverDirectory != a.DriverDirectory {
		t.Fatal("URI lost immutable native driver pin", err)
	}
	for _, mutate := range []func(*connections.TrinoOptions){
		func(a *connections.TrinoOptions) { a.Mode = "postgres" },
		func(a *connections.TrinoOptions) { a.Version = 2 },
		func(a *connections.TrinoOptions) { a.DriverSHA256 = "" },
		func(a *connections.TrinoOptions) { a.DriverSHA256 = strings.Repeat("A", 64) },
		func(a *connections.TrinoOptions) { a.DriverVersion = "../outside" },
		func(a *connections.TrinoOptions) { a.DriverDirectory = "relative" },
		func(a *connections.TrinoOptions) { a.MetadataReserve = 4 },
		func(a *connections.TrinoOptions) { a.SQLMaxConnections = 5 },
		func(a *connections.TrinoOptions) { a.PoolWaitTimeoutMilliseconds = 1 },
		func(a *connections.TrinoOptions) { a.ConnectTimeoutSeconds = 301 },
		func(a *connections.TrinoOptions) { a.QueryTimeoutSeconds = -1 },
	} {
		bad := a
		mutate(&bad)
		if _, err := connections.NormalizeTrinoOptions(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid native settings accepted", bad, err)
		}
	}
	for _, uri := range []string{"trino://localhost?mode=jdbc&mode=http", "trino://localhost?mode=jdbc&driver_version=471", "trino://localhost?mode=postgres"} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatal("invalid driver URI accepted", uri)
		}
	}
}
