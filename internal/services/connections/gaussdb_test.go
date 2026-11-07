package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestGaussDBNativeURIProfilesAndBoundaries(t *testing.T) {
	for _, spec := range []struct{ uri, host, additional string }{
		{"gaussdb://reader:private%40secret@first:5433,second/app?sslmode=require&target_session_attrs=any", "first", "second:5432"},
		{"gaussdb://reader:private%40secret@[::1]:5433,[2001:db8::1]/app", "::1", "[2001:db8::1]:5432"},
		{"GAUSSDB://reader:private%40secret@[::1],host:5440/app", "::1", "host:5440"},
	} {
		i, err := connections.ParseURI(spec.uri)
		if err != nil || i.Profile.Kind != connections.GaussDB || i.Profile.Host != spec.host || i.Profile.GaussDBOptions.AdditionalEndpoints != spec.additional || i.Password != "private@secret" {
			t.Fatal("native URI lost identity or endpoints", err, i.Profile)
		}
		raw, _ := json.Marshal(i.Profile)
		if strings.Contains(string(raw), "private") || strings.Contains(i.String(), "private") {
			t.Fatal("URI secret escaped temporary storage")
		}
	}
	p, _ := connections.Defaults(connections.GaussDB)
	p.Name = "native"
	if p.Port != 5432 || p.TLSMode != "verify-full" || p.GaussDBOptions.TargetSessionAttrs != "read-write" || !p.Kind.IsGaussFamily() {
		t.Fatal("native defaults", p)
	}
	d, _ := connections.Describe(p.Kind)
	if !d.Capabilities.EditableTables || !d.Capabilities.Import || !d.Capabilities.Explain || d.Capabilities.DesignTables || d.Capabilities.DesignViews || d.Capabilities.Objects || d.Capabilities.Accounts || d.Capabilities.Backup || d.Capabilities.Maintenance {
		t.Fatal("unverified administration capability exposed")
	}
	for _, set := range []func(*connections.Profile){
		func(p *connections.Profile) { p.RedisOptions = connections.DefaultRedisOptions() },
		func(p *connections.Profile) { p.MySQLAdvanced.ConnectTimeoutSeconds = 1 },
		func(p *connections.Profile) { p.PostgreSQLOptions.LockTimeoutMS = 1 },
		func(p *connections.Profile) { p.GaussDBOptions.AdditionalEndpoints = "localhost:5432" },
		func(p *connections.Profile) { p.GaussDBOptions.Version = 2 },
		func(p *connections.Profile) { p.Kind = connections.OpenGauss },
	} {
		bad := p
		set(&bad)
		if _, err := connections.NormalizeProfile(bad); err == nil {
			t.Fatal("foreign or unsupported options accepted")
		}
	}
	for _, uri := range []string{
		"gaussdb:reader@host", "gaussdb://reader:private-secret@host:0/app", "gaussdb://reader@host,host/app",
		"gaussdb://reader@host/app?sslmode=require&sslmode=disable", "gaussdb://reader@host/app?unknown=private-secret",
		"gaussdb://reader@[bad]/app", "gaussdb://reader@host/app#fragment", "gaussdb://reader@host/app/other",
	} {
		_, err := connections.ParseURI(uri)
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid URI accepted or credentials disclosed")
		}
	}
}
