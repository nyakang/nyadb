package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLHTTPValidationAndTunnelExclusivity(t *testing.T) {
	store, _, _ := openStore(t)
	for _, change := range []func(*connections.MySQLAdvanced){
		func(a *connections.MySQLAdvanced) { a.HTTP.Enabled = true; a.HTTP.ProxyPort = 0 },
		func(a *connections.MySQLAdvanced) { a.HTTP.ProxyPort = 65536 },
		func(a *connections.MySQLAdvanced) { a.HTTP.URL = "https://name:secret@host/tunnel.php" },
		func(a *connections.MySQLAdvanced) { a.HTTP.URL = "https://host/tunnel?token=secret" },
		func(a *connections.MySQLAdvanced) { a.HTTP.URL = "javascript:alert(1)" },
		func(a *connections.MySQLAdvanced) { a.HTTP.ProxyHost = "line\nbreak" },
		func(a *connections.MySQLAdvanced) { a.HTTP.ClientKey = strings.Repeat("x", 4097) },
		func(a *connections.MySQLAdvanced) { a.SSH.Enabled = true; a.HTTP.Enabled = true },
	} {
		a := connections.DefaultMySQLAdvanced()
		change(&a)
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "invalid", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted invalid HTTP configuration: %+v %v", a.HTTP, err)
		}
	}
}
