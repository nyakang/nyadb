package connections_test

import (
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestOracleProfileURIAndOptions(t *testing.T) {
	imported, err := connections.ParseURI("oracle://tenant:temporary-secret@localhost:1521/XEPDB1?connection_type=service&schema=TENANT")
	if err != nil {
		t.Fatal(err)
	}
	p := imported.Profile
	if p.Kind != connections.Oracle || p.Port != 1521 || p.OracleOptions.DefaultSchema != "TENANT" || imported.Password != "temporary-secret" || strings.Contains(fmt.Sprintf("%+v", imported), "temporary-secret") {
		t.Fatal("Oracle URI/settings/redaction")
	}
	for _, modify := range []func(*connections.Profile){func(p *connections.Profile) { p.OracleOptions.MaxConnections = 65 }, func(p *connections.Profile) { p.OracleOptions.ConnectionType = "tns" }, func(p *connections.Profile) { p.OracleOptions.ApplicationName = "bad)(PROGRAM=x" }, func(p *connections.Profile) { p.TLSMode = "prefer" }, func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() }, func(p *connections.Profile) { p.Host = "user@host" }} {
		bad := p
		modify(&bad)
		if _, err := connections.NormalizeProfile(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid Oracle options accepted", err)
		}
	}
	if _, err = connections.ParseURI("oracle://tenant:secret@localhost/XEPDB1?wallet=secret"); !errors.Is(err, connections.ErrURIOptions) {
		t.Fatal(err)
	}
}
