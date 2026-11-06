package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCockroachProductURIOptionsAndCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.CockroachDB)
	if err != nil || p.Port != 26257 || p.Database != "defaultdb" || p.TLSMode != "verify-full" || !p.Kind.IsPostgreSQLFamily() || p.Kind.IsMySQLFamily() {
		t.Fatal("defaults", err, p)
	}
	i, err := connections.ParseURI("cockroachdb://reader:private%40secret@cluster.example/app?sslmode=verify-full")
	if err != nil || i.Profile.Kind != connections.CockroachDB || i.Profile.Port != 26257 || i.Password != "private@secret" {
		t.Fatal("URI identity", err)
	}
	alias, err := connections.ParseURI("cockroach://root@localhost")
	if err != nil || alias.Profile.Database != "defaultdb" || alias.Profile.Kind != connections.CockroachDB || alias.Profile.TLSMode != "verify-full" {
		t.Fatal("URI alias/default database", err)
	}
	raw, _ := json.Marshal(i.Profile)
	if strings.Contains(string(raw), "private") {
		t.Fatal("secret persisted")
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.ClientFamily != "postgres" || d.Dialect != "postgres" || len(d.FieldTypes()) == 0 || !d.Capabilities.Transactions || !d.Capabilities.Explain || !d.Capabilities.Import || d.Capabilities.DesignTables || d.Capabilities.Backup || d.Capabilities.Accounts {
		t.Fatal("product capability contract")
	}
	p.Name = "fixture"
	p.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	p.PostgreSQLOptions.DefaultSchema = "app"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal("shared public options", err)
	}
	p.Kind = connections.MySQL
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("foreign options accepted")
	}
	for _, uri := range []string{"cockroachdb://reader@host:0/app", "cockroachdb://reader@host/app?unknown=secret", "cockroachdb://reader@host/app?sslmode=invalid"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("invalid URI accepted")
		}
	}
}
