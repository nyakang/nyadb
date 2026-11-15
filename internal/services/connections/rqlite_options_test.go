package connections_test

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestRQLiteOriginalEndpointAndCredential(t *testing.T) {
	u, err := connections.ParseRQLiteURI("rqlites://%20reader%20:s%3A%0Asecret@[::1]:4002/proxy/rqlite?consistency=none&freshness_milliseconds=250&freshness_strict=true&batch_transaction=true")
	if err != nil {
		t.Fatal(err)
	}
	if u.Profile.Username != " reader " || u.Password != "s:\nsecret" || u.Profile.Database != "main" || u.Profile.RQLiteOptions.Consistency != "none" || !u.Profile.RQLiteOptions.BatchTransaction {
		t.Fatal("original inputs were changed")
	}
	c, err := connections.RQLiteProfileConfig(u.Profile)
	if err != nil || c.Endpoint != "https://[::1]:4002/proxy/rqlite" {
		t.Fatalf("endpoint=%q err=%v", c.Endpoint, err)
	}
	d, ok := connections.Describe(connections.RQLite)
	if !ok || d.Dialect != "sqlite" || d.ClientFamily != "rqlite" || d.FormKind != "server" || d.DefaultPort != 4001 || u.Profile.Kind.IsREST() {
		t.Fatal("RQLite must retain HTTP transport and SQL presentation")
	}
}

func TestRQLiteRejectUnsafeOrAmbiguousConfiguration(t *testing.T) {
	for _, uri := range []string{
		"rqlite://host?retries=1", "rqlite://host?queue", "rqlite://host?redirect", "rqlite://host?consistency=none&consistency=weak", "rqlite://host?password=secret", "rqlite://host?tls_mode=require", "rqlites://host?tls_mode=disable", "rqlite://host?freshness_strict=true", "rqlite://host?freshness_milliseconds=1", "rqlite://host?request_timeout_seconds=30", "rqlite://host/a/../b", "rqlite://host//b", "rqlite://user%3Aname:secret@host", "rqlite://host?max_requests=01", "rqlite://host?auth=basic", "rqlite://host?ca_file=/tmp/root.pem", "rqlite://host#", "rqlite://host?auth=NONE",
	} {
		t.Run(uri, func(t *testing.T) {
			if _, err := connections.ParseRQLiteURI(uri); !errors.Is(err, connections.ErrURIOptions) {
				t.Fatalf("unsafe URI admitted: %v", err)
			}
		})
	}
	p, _ := connections.Defaults(connections.RQLite)
	p.Name = "original"
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Kind = connections.MySQL },
		func(p *connections.Profile) { p.Database = "other" },
		func(p *connections.Profile) { p.Host = "user@host" },
		func(p *connections.Profile) { p.Host = `"invalid-host"` },
		func(p *connections.Profile) { p.RQLiteOptions.Version = 2 },
		func(p *connections.Profile) { p.RQLiteOptions.ClientCertFile = "/tmp/cert.pem" },
		func(p *connections.Profile) { p.RQLiteOptions.SQLMaxConnections = 9 },
		func(p *connections.Profile) { p.RQLiteOptions.DatabaseTimeoutMilliseconds = 60000 },
	} {
		q := p
		change(&q)
		if _, err := connections.NormalizeProfile(q); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid options admitted")
		}
	}
}
