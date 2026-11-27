package connections_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDatabendProfilePreservesExplicitOriginalPackageAndTarget(t *testing.T) {
	p, err := connections.Defaults(connections.Databend)
	if err != nil || p.Port != 8000 || p.Username != "databend" || p.Database != "default" {
		t.Fatal("original product defaults", err)
	}
	p.Name = "original Databend"
	p.Database = `db_under_"quote`
	p.Host = "::1"
	p.Username = " original user "
	p.TLSMode = "verify-full"
	a := &p.DatabendOptions
	a.DriverVersion = "jdbc-0.4.8-agent-0.1.82"
	a.DriverSHA256 = strings.Repeat("a", 64)
	a.DriverDirectory = t.TempDir()
	a.CAFile = filepath.Join(t.TempDir(), "original-ca.pem")
	a.ServerName = "original.example"
	a.MaxConnections = 5
	a.MetadataReserve = 2
	a.SQLMaxConnections = 2
	a.PoolWaitMillis = 1700
	n, err := connections.NormalizeProfile(p)
	if err != nil || n.Database != p.Database || n.Username != p.Username || n.DatabendOptions != p.DatabendOptions {
		t.Fatal("original identity/options changed", err)
	}
	c, err := connections.DatabendProfileManagedConfig(n)
	if err != nil || c.Transport.Endpoint != "https://[::1]:8000" || c.Database != p.Database || c.DriverSHA256 != a.DriverSHA256 || c.MaxConnections != 5 || c.MetadataReserve != 2 {
		t.Fatal("managed config changed captured settings", err)
	}
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Kind = connections.PostgreSQL },
		func(p *connections.Profile) { p.Host = "secret@original" },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.DatabendOptions.DriverSHA256 = "" },
		func(p *connections.Profile) { p.DatabendOptions.DriverVersion = "../replacement" },
		func(p *connections.Profile) { p.DatabendOptions.ClientCertFile = "relative.pem" },
		func(p *connections.Profile) { p.DatabendOptions.SQLMaxConnections = 4 },
		func(p *connections.Profile) { p.DatabendOptions.RequestTimeoutSeconds = 3601 },
	} {
		bad := p
		change(&bad)
		if _, err := connections.NormalizeProfile(bad); err == nil {
			t.Fatal("ambiguous/cross-engine settings accepted")
		}
	}
	if imported, err := connections.ParseURI("databend://original:8000/default"); err != nil || imported.Profile.Kind != connections.Databend || imported.Profile.Port != 8000 || imported.Profile.TLSMode != "verify-full" || imported.Profile.DatabendOptions.DriverVersion != "" {
		t.Fatal("public URI changed original target or implicitly selected a driver", err)
	}
}
