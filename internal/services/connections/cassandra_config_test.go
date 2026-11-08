package connections_test

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCassandraConfigurationAndCredentialBoundaries(t *testing.T) {
	base := connections.CassandraConfig{Endpoint: "[::1]:09042"}
	c, err := connections.NormalizeCassandraConfig(base)
	if err != nil || c.Endpoint != "[::1]:9042" || c.Auth != "none" || c.TLSMode != "verify-full" {
		t.Fatalf("normalization: %+v %v", c, err)
	}
	for _, mutate := range []func(*connections.CassandraConfig){
		func(c *connections.CassandraConfig) { c.Endpoint = "localhost" },
		func(c *connections.CassandraConfig) { c.Endpoint = "localhost:0" },
		func(c *connections.CassandraConfig) { c.Endpoint = "user@localhost:9042" },
		func(c *connections.CassandraConfig) { c.Endpoint = "https://localhost:9042" },
		func(c *connections.CassandraConfig) { c.Endpoint = "local host:9042" },
		func(c *connections.CassandraConfig) { c.Auth = "basic" },
		func(c *connections.CassandraConfig) { c.Auth = "password" },
		func(c *connections.CassandraConfig) { c.Username = "secret" },
		func(c *connections.CassandraConfig) { c.TLSMode = "require" },
		func(c *connections.CassandraConfig) { c.CAFile = "relative.pem" },
		func(c *connections.CassandraConfig) { c.TLSMode = "disable"; c.ServerName = "host" },
		func(c *connections.CassandraConfig) { c.ServerName = "host\n" },
	} {
		v := base
		mutate(&v)
		if _, err := connections.NormalizeCassandraConfig(v); err == nil {
			t.Fatalf("accepted invalid configuration: %+v", v)
		}
	}
	c.Auth, c.Username = "password", "native-user"
	for _, secret := range []string{"", "embedded\x00value", strings.Repeat("a", 4097), "\xff"} {
		if connections.ValidateCassandraCredential(c, secret, true) == nil {
			t.Fatal("accepted invalid credential")
		}
	}
	if connections.ValidateCassandraCredential(c, "valid-separate-secret", true) != nil {
		t.Fatal("valid password rejected")
	}
	c.Auth = "none"
	if connections.ValidateCassandraCredential(c, "separate-secret", false) == nil {
		t.Fatal("none accepted a credential")
	}
}
