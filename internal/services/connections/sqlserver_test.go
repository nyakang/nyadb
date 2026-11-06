package connections_test

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLServerDefaultsURIAndStrictOptions(t *testing.T) {
	p, err := connections.Defaults(connections.SQLServer)
	if err != nil || p.Port != 1433 || p.Database != "master" || p.TLSMode != "verify-full" {
		t.Fatal(p, err)
	}
	p.Name = "SQL Server"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	v, err := connections.ParseURI("sqlserver://reader:temporary@localhost/tenant?sslmode=require")
	if err != nil || v.Profile.Kind != connections.SQLServer || v.Profile.Database != "tenant" || v.Password != "temporary" || v.Profile.Port != 1433 {
		t.Fatal("URI", err)
	}
	v, err = connections.ParseURI("sqlserver://reader:temporary@localhost:1433?database=tenant&encrypt=true&TrustServerCertificate=true")
	if err != nil || v.Profile.Database != "tenant" || v.Profile.TLSMode != "require" {
		t.Fatal("standard TDS URI", err)
	}
	for _, uri := range []string{"sqlserver://sa@host/db?database=other", "sqlserver://sa@host/db?encrypt=true&sslmode=disable", "sqlserver://sa@host/db?trustservercertificate=true&TrustServerCertificate=false"} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatal("ambiguous URI accepted")
		}
	}
	for _, change := range []func(*connections.Profile){func(p *connections.Profile) { p.Database = "" }, func(p *connections.Profile) { p.Username = "" }, func(p *connections.Profile) { p.TLSMode = "prefer" }, func(p *connections.Profile) { p.Host = "host\\instance" }, func(p *connections.Profile) { p.SQLServerOptions.MaxConnections = 0 }, func(p *connections.Profile) { p.SQLServerOptions.Version = 2 }, func(p *connections.Profile) { p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions() }} {
		bad := p
		change(&bad)
		if _, err = connections.NormalizeProfile(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid option accepted", err)
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "other"
	other.SQLServerOptions = p.SQLServerOptions
	if _, err = connections.NormalizeProfile(other); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign engine options accepted")
	}
}
