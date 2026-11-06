package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestOpenGaussProductURIOptionsAndCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.OpenGauss)
	if err != nil || p.Port != 5432 || p.Database != "postgres" || p.TLSMode != "verify-full" || !p.Kind.IsPostgreSQLFamily() || p.Kind.IsMySQLFamily() {
		t.Fatal("defaults", err, p)
	}
	i, err := connections.ParseURI("opengauss://reader:private%40secret@cluster.example/app?sslmode=verify-full")
	if err != nil || i.Profile.Kind != connections.OpenGauss || i.Profile.Port != 5432 || i.Password != "private@secret" {
		t.Fatal("URI identity", err)
	}
	alias, err := connections.ParseURI("opengauss://root@localhost")
	if err != nil || alias.Profile.Database != "postgres" || alias.Profile.Kind != connections.OpenGauss || alias.Profile.TLSMode != "verify-full" {
		t.Fatal("URI alias/default database", err)
	}
	raw, _ := json.Marshal(i.Profile)
	if strings.Contains(string(raw), "private") {
		t.Fatal("secret persisted")
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.ClientFamily != "postgres" || d.Dialect != "postgres" || len(d.FieldTypes()) == 0 || !d.Capabilities.Transactions || !d.Capabilities.Explain || !d.Capabilities.Import || !d.Capabilities.DesignViews || !d.Capabilities.Objects || d.Capabilities.DesignTables || d.Capabilities.Backup || d.Capabilities.Accounts {
		t.Fatal("product capability contract")
	}
	p.Name = "fixture"
	p.PostgreSQLOptions.CAFile = "/fixture/ca.crt"
	p.PostgreSQLOptions.DefaultSchema = "app"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal("shared public options", err)
	}
	for _, set := range []func(*connections.Profile){func(v *connections.Profile) { v.PostgreSQLOptions.LockTimeoutMS = 1 }, func(v *connections.Profile) { v.PostgreSQLOptions.ToolsDirectory = "/pg/tools" }, func(v *connections.Profile) { v.Database = "" }, func(v *connections.Profile) { v.Username = "" }} {
		bad := p
		set(&bad)
		if _, e := connections.NormalizeProfile(bad); e == nil {
			t.Fatal("unsupported configuration accepted")
		}
	}
	p.Kind = connections.MySQL
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("foreign options accepted")
	}
	for _, uri := range []string{"opengauss://reader@host:0/app", "opengauss://reader@host/app?unknown=secret", "opengauss://reader@host/app?sslmode=invalid"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("invalid URI accepted")
		}
	}
}
