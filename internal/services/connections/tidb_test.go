package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTiDBIndependentIdentityURIAndActiveConfiguration(t *testing.T) {
	imported, err := connections.ParseURI("tidb://user:private%40secret@cluster.example:4001/app?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	p := imported.Profile
	if p.Kind != connections.TiDB || p.Port != 4001 || p.TLSMode != "verify-full" || imported.Password != "private@secret" {
		t.Fatal("TiDB URI lost product or settings")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("profile stored a credential")
	}
	p.ID = 71
	p.Variants = []connections.ConnectionVariant{{Name: "secondary", Host: "second.example", Port: 4002, Database: "app", Username: "alternate", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
	p.ActiveVariant = "secondary"
	s, err := connections.ResolveActiveSettings(p)
	if err != nil || s.Kind != connections.TiDB || s.Port != 4002 || s.ConfigurationName != "secondary" {
		t.Fatal("active configuration lost TiDB identity", err)
	}
	defaults, err := connections.Defaults(connections.TiDB)
	if err != nil || defaults.Port != 4000 || !defaults.Kind.IsMySQLFamily() {
		t.Fatal("TiDB defaults", err)
	}
	d, ok := connections.Describe(connections.TiDB)
	if !ok || d.Label != "TiDB" || d.DefaultPort != 4000 || d.ClientFamily != "mysql" || !d.Capabilities.DesignTables || !d.Capabilities.Transactions || len(d.FieldTypes()) == 0 {
		t.Fatal("incomplete TiDB descriptor")
	}
	for _, uri := range []string{"tidb://host:0/app", "tidb://host/app?sslmode=verify-ca", "tidb://host/app?unknown=secret"} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatal("invalid TiDB URI accepted")
		}
	}
}
