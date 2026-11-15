package connections_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQuestDBNativeProfileAndURIIdentity(t *testing.T) {
	u, err := connections.ParseURI("questdb://questdb:original%3Asecret@[::1]:8813/qdb?query_timeout=120&max_connections=7&sql_max_connections=3&read_only=true")
	if err != nil || u.Password != "original:secret" || u.Profile.Kind != connections.QuestDB || u.Profile.Database != "qdb" || u.Profile.Port != 8813 || !u.Profile.QuestDBOptions.ReadOnly || u.Profile.QuestDBOptions.QueryTimeoutSeconds != 120 || u.Profile.QuestDBOptions.MaxConnections != 7 || u.Profile.QuestDBOptions.SQLMaxConnections != 3 {
		t.Fatal("native profile changed", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", u, u), "original:secret") || u.Profile.Kind.IsPostgreSQLFamily() {
		t.Fatal("QuestDB credentials or semantic family exposed")
	}
	d, _ := connections.Describe(connections.QuestDB)
	if d.DefaultPort != 8812 || d.Dialect != "questdb" || !d.Capabilities.Query || !d.Capabilities.Export || d.Capabilities.EditableTables || d.Capabilities.Transactions || d.Capabilities.Keyset || d.Capabilities.DesignTables {
		t.Fatal("unimplemented native semantics advertised")
	}
	for _, raw := range []string{"questdb://host/other", "questdb://host:0", "questdb://host?password=secret", "questdb://host?schema=public", "questdb://host?query_timeout=0", "questdb://host?query_timeout=1&query_timeout=2", "questdb://host?read_only=1", "questdb://host?sslmode=prefer", "questdb://host?ca_file=/tmp/root", "questdb://host#fragment"} {
		if _, e := connections.ParseURI(raw); e == nil {
			t.Fatal("ambiguous or unsupported URI admitted", raw)
		}
	}
	p, _ := connections.Defaults(connections.PostgreSQL)
	p.Name = "other"
	p.QuestDBOptions = u.Profile.QuestDBOptions
	if _, err = connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign product options admitted", err)
	}
}
