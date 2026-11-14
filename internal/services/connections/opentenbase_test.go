package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestOpenTenBaseProfileURIAndCapabilityIdentity(t *testing.T) {
	p, err := connections.Defaults(connections.OpenTenBase)
	if err != nil || !p.Kind.IsPostgreSQLFamily() || p.Port != 11000 || p.Username != "opentenbase" {
		t.Fatal("OpenTenBase defaults", err)
	}
	p.Name = "OpenTenBase"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.Label != "OpenTenBase" || d.Dialect != "postgres" || !d.Capabilities.EditableTables || !d.Capabilities.Explain || d.Capabilities.DesignTables || d.Capabilities.Backup || d.Capabilities.Import {
		t.Fatal("product identity or unverified capabilities changed")
	}
	uri, err := connections.ParseURI("opentenbase://reader:private%40secret@[::1]:5433/analytics?sslmode=verify-full&schema=tenant")
	if err != nil || uri.Profile.Kind != p.Kind || uri.Profile.Host != "::1" || uri.Profile.Port != 5433 || uri.Profile.Database != "analytics" || uri.Password != "private@secret" || uri.Profile.PostgreSQLOptions.DefaultSchema != "tenant" {
		t.Fatal("OpenTenBase URI changed original identity", err)
	}
	raw, _ := json.Marshal(uri)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("temporary URI secret serialized")
	}
	p.PostgreSQLOptions.ToolsDirectory = "/postgres/tools"
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("generic PostgreSQL tools accepted for OpenTenBase")
	}
	p.PostgreSQLOptions.ToolsDirectory = ""
	p.MySQLAdvanced = connections.DefaultMySQLAdvanced()
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("MySQL options accepted for OpenTenBase")
	}
}
