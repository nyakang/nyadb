package connections_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTDengineNativeProfileAndURIIdentity(t *testing.T) {
	for _, prefix := range []string{"tdengine://", "taosws://", "taoswss://", "ws://", "wss://", "jdbc:TAOS-WS://", "jdbc:TAOS-RS://"} {
		imported, err := connections.ParseURI(prefix + "root:temporary%3Asecret@localhost/metrics")
		if err != nil || imported.Profile.Kind != connections.TDengine || imported.Password != "temporary:secret" || imported.Profile.Database != "metrics" {
			t.Fatal("native URI alias changed original connection", prefix, err)
		}
		if (prefix == "wss://" || prefix == "taoswss://") && imported.Profile.TLSMode != "verify-full" {
			t.Fatal("secure alias lost certificate verification")
		}
	}
	if _, err := connections.ParseURI("taoswss://host?sslmode=disable"); err == nil {
		t.Fatal("secure URI allowed transport downgrade")
	}
	u, err := connections.ParseURI("tdengine://tdengine:original%3Asecret@[::1]:6042/metrics?query_timeout=120&max_connections=7&sql_max_connections=3&read_only=true")
	if err != nil || u.Password != "original:secret" || u.Profile.Kind != connections.TDengine || u.Profile.Database != "metrics" || u.Profile.Port != 6042 || !u.Profile.TDengineOptions.ReadOnly || u.Profile.TDengineOptions.QueryTimeoutSeconds != 120 || u.Profile.TDengineOptions.MaxConnections != 7 || u.Profile.TDengineOptions.SQLMaxConnections != 3 {
		t.Fatal("native profile changed", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", u, u), "original:secret") || u.Profile.Kind.IsPostgreSQLFamily() {
		t.Fatal("TDengine credentials or semantic family exposed")
	}
	d, _ := connections.Describe(connections.TDengine)
	if d.DefaultPort != 6041 || d.Dialect != "tdengine" || !d.Capabilities.Query || !d.Capabilities.Export || !d.Capabilities.EditableTables || d.Capabilities.Transactions || d.Capabilities.Keyset || d.Capabilities.DesignTables {
		t.Fatal("unimplemented native semantics advertised")
	}
	for _, raw := range []string{"tdengine://host:0", "tdengine://host?password=secret", "tdengine://host?schema=public", "tdengine://host?query_timeout=0", "tdengine://host?query_timeout=1&query_timeout=2", "tdengine://host?read_only=1", "tdengine://host?sslmode=prefer", "tdengine://host?ca_file=/tmp/root", "tdengine://host#fragment"} {
		if _, e := connections.ParseURI(raw); e == nil {
			t.Fatal("ambiguous or unsupported URI admitted", raw)
		}
	}
	p, _ := connections.Defaults(connections.PostgreSQL)
	p.Name = "other"
	p.TDengineOptions = u.Profile.TDengineOptions
	if _, err = connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign product options admitted", err)
	}
}
