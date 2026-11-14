package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLProductsPreserveURIIdentityAndActiveConfiguration(t *testing.T) {
	for _, kind := range []connections.Kind{connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt} {
		t.Run(string(kind), func(t *testing.T) {
			imported, err := connections.ParseURI(string(kind) + "://reader:private%40secret@[::1]:3307/app?sslmode=verify-full")
			if err != nil {
				t.Fatal(err)
			}
			p := imported.Profile
			if p.Kind != kind || p.Port != 3307 || p.Host != "::1" || p.Database != "app" || p.TLSMode != "verify-full" || imported.Password != "private@secret" {
				t.Fatal("URI changed product, target or credential")
			}
			raw, err := json.Marshal(imported)
			if err != nil || strings.Contains(string(raw), "secret") {
				t.Fatal("URI import serialized its temporary credential")
			}
			p.Variants = []connections.ConnectionVariant{{Name: "Replica", Host: "replica.example", Port: 3308, Database: "other", Username: "alternate", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
			p.ActiveVariant = "replica"
			settings, err := connections.ResolveActiveSettings(p)
			if err != nil || settings.Kind != kind || settings.ConfigurationName != "Replica" || settings.Host != "replica.example" || settings.Port != 3308 || settings.Database != "other" {
				t.Fatal("active configuration lost its original product or target", err)
			}
			if p.ActiveVariant != "replica" {
				t.Fatal("resolution changed original profile")
			}
			defaults, err := connections.Defaults(kind)
			if err != nil || defaults.Kind != kind || defaults.Port != 3306 || !kind.IsMySQLFamily() {
				t.Fatal("product defaults", err)
			}
			descriptor, ok := connections.Describe(kind)
			if !ok || descriptor.Kind != kind || descriptor.ClientFamily != "mysql" || descriptor.Dialect != "mysql" || len(descriptor.FieldTypes()) == 0 || !descriptor.Capabilities.Query || !descriptor.Capabilities.EditableTables || !descriptor.Capabilities.Transactions || descriptor.Capabilities.Accounts || descriptor.Capabilities.Backup || descriptor.Capabilities.Restore || descriptor.Capabilities.Maintenance {
				t.Fatal("unverified product administration was advertised")
			}
			for _, suffix := range []string{"?unknown=private-secret", "?sslmode=prefer", "?sslmode=verify-ca"} {
				if _, err := connections.ParseURI(string(kind) + "://reader:private-secret@host/app" + suffix); err == nil || strings.Contains(err.Error(), "private-secret") {
					t.Fatal("unsupported URI option accepted or credential exposed")
				}
			}
			defaults.Name = "foreign options"
			defaults.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
			if _, err := connections.NormalizeProfile(defaults); err == nil {
				t.Fatal("foreign engine options accepted")
			}
		})
	}
}
