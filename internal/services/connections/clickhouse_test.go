package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestClickHouseProfileDefaultsAndCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.ClickHouse)
	p.Name = "native"
	if err != nil || p.Port != 9000 || p.Database != "default" || p.Username != "default" {
		t.Fatal("missing native defaults")
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	d, _ := connections.Describe(p.Kind)
	if !d.Capabilities.Query || !d.Capabilities.Metadata || !d.Capabilities.Count || !d.Capabilities.Export || d.Capabilities.Transactions || d.Capabilities.EditableTables || d.Capabilities.DesignTables || d.Capabilities.Import || d.Capabilities.Backup || d.Capabilities.Keyset {
		t.Fatal("unsupported relational feature enabled")
	}
	for _, mode := range []string{"prefer", "require", "verify-ca"} {
		bad := p
		bad.TLSMode = mode
		if _, err = connections.NormalizeProfile(bad); err == nil {
			t.Fatal("TLS verification downgrade accepted")
		}
	}
	p.TLSMode = "verify-full"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	p.MySQLAdvanced = connections.DefaultMySQLAdvanced()
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("MySQL settings accepted on ClickHouse")
	}
}
func TestClickHouseURIUsesNativeDefaultsAndTemporarySecrets(t *testing.T) {
	parsed, err := connections.ParseURI("clickhouse://tester:temporary@localhost/database?sslmode=verify-full")
	if err != nil || parsed.Profile.Kind != connections.ClickHouse || parsed.Profile.Port != 9000 || parsed.Profile.TLSMode != "verify-full" || parsed.Password != "temporary" {
		t.Fatal("native URI failed")
	}
	parsed, err = connections.ParseURI("clickhouse://localhost")
	if err != nil || parsed.Profile.Username != "default" || parsed.Profile.Database != "default" {
		t.Fatal("URI defaults failed")
	}
	if _, err = connections.ParseURI("clickhouse://localhost?sslmode=require"); err == nil {
		t.Fatal("unsupported insecure TLS accepted")
	}
}
