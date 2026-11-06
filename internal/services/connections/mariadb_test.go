package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMariaDBIdentityURIAndActiveSettings(t *testing.T) {
	imported, err := connections.ParseURI("mariadb://reader:private-secret@[::1]:3307/research?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	p := imported.Profile
	if p.Kind != connections.MariaDB || p.Port != 3307 || p.TLSMode != "verify-full" || imported.Password != "private-secret" {
		t.Fatal("MariaDB URI lost identity or connection settings")
	}
	raw, _ := json.Marshal(imported)
	if strings.Contains(string(raw), "private-secret") {
		t.Fatal("URI persisted password")
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.Label != "MariaDB" || d.ClientFamily != "mysql" || d.Dialect != "mysql" || d.DefaultPort != 3306 || d.Capabilities.Accounts || !d.Capabilities.EditableTables || len(d.FieldTypes()) == 0 {
		t.Fatal("MariaDB descriptor lost product or feature boundaries")
	}
	p.Variants = []connections.ConnectionVariant{{Name: "Replica", Host: "replica.example", Port: 3308, Username: "replica", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
	p.ActiveVariant = "replica"
	s, err := connections.ResolveActiveSettings(p)
	if err != nil || s.Kind != connections.MariaDB || s.Host != "replica.example" || s.ConfigurationName != "Replica" || s.Port != 3308 {
		t.Fatalf("MariaDB active configuration: %+v %v", s, err)
	}
	if p.ActiveVariant != "replica" || p.Variants[0].Name != "Replica" {
		t.Fatal("resolution mutated original profile")
	}
	for _, uri := range []string{"mariadb://user:private-secret@host/db?sslmode=prefer", "mariadb://user:private-secret@host:0/db"} {
		if _, err := connections.ParseURI(uri); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid MariaDB URI accepted or exposed secret")
		}
	}
}

func TestMariaDBSettingsRejectForeignOptionsAndRetainMySQLDefaults(t *testing.T) {
	p, err := connections.Defaults(connections.MariaDB)
	if err != nil || p.Kind != connections.MariaDB || p.Port != 3306 || p.MySQLAdvanced != connections.DefaultMySQLAdvanced() {
		t.Fatal("MariaDB defaults")
	}
	p.Name = "MariaDB"
	p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
	if _, err := connections.NormalizeProfile(p); err == nil {
		t.Fatal("foreign options accepted")
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.MariaDB, connections.PostgreSQL, connections.SQLite, connections.ClickHouse, "unknown"} {
		if kind.IsMySQLFamily() != (kind == connections.MySQL || kind == connections.MariaDB) {
			t.Fatalf("wrong client family for %s", kind)
		}
	}
}
