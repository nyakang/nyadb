package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestOceanBaseIndependentIdentityURIAndActiveConfiguration(t *testing.T) {
	imported, err := connections.ParseURI("oceanbase://user:private%40secret@cluster.example:2882/app?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	p := imported.Profile
	if p.Kind != connections.OceanBase || p.Port != 2882 || p.TLSMode != "verify-full" || imported.Password != "private@secret" {
		t.Fatal("OceanBase URI lost product or settings")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("profile stored a credential")
	}
	p.ID = 71
	p.Variants = []connections.ConnectionVariant{{Name: "secondary", Host: "second.example", Port: 2883, Database: "app", Username: "alternate", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
	p.ActiveVariant = "secondary"
	s, err := connections.ResolveActiveSettings(p)
	if err != nil || s.Kind != connections.OceanBase || s.Port != 2883 || s.ConfigurationName != "secondary" {
		t.Fatal("active configuration lost OceanBase identity", err)
	}
	defaults, err := connections.Defaults(connections.OceanBase)
	if err != nil || defaults.Port != 2881 || !defaults.Kind.IsMySQLFamily() {
		t.Fatal("OceanBase defaults", err)
	}
	d, ok := connections.Describe(connections.OceanBase)
	if !ok || d.Label != "OceanBase" || d.DefaultPort != 2881 || d.ClientFamily != "mysql" || !d.Capabilities.Transactions || len(d.FieldTypes()) == 0 {
		t.Fatal("incomplete OceanBase descriptor")
	}
	for _, uri := range []string{"oceanbase://host:0/app", "oceanbase://host/app?sslmode=verify-ca", "oceanbase://host/app?unknown=secret"} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatal("invalid OceanBase URI accepted")
		}
	}
}
