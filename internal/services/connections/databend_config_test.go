package connections_test

import (
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDatabendOriginalTargetConfiguration(t *testing.T) {
	c, err := connections.NormalizeDatabendConfig(connections.DatabendConfig{Endpoint: "http://[::1]:8000/", Username: "root"})
	if err != nil || c.Endpoint != "http://[::1]:8000" || c.Auth != "basic" || c.ConnectTimeoutSeconds != 15 || c.RequestTimeoutSeconds != 120 {
		t.Fatal("original HTTP target/defaults changed", c, err)
	}
	dir := t.TempDir()
	original := connections.DatabendConfig{
		Endpoint: "https://warehouse.example:443", Auth: "bearer", ServerName: "warehouse.example",
		CAFile: filepath.Join(dir, "ca.pem"), ClientCertFile: filepath.Join(dir, "client.pem"), ClientKeyFile: filepath.Join(dir, "key.pem"),
		ConnectTimeoutSeconds: 7, RequestTimeoutSeconds: 33,
	}
	if c, err := connections.NormalizeDatabendConfig(original); err != nil || c != original {
		t.Fatal("captured TLS/auth configuration changed", err)
	}
}

func TestDatabendRejectsAmbiguousTargetsAndCredentialConfiguration(t *testing.T) {
	base := connections.DatabendConfig{Endpoint: "https://original.example:8000", Username: "root"}
	for _, mutate := range []func(*connections.DatabendConfig){
		func(c *connections.DatabendConfig) { c.Endpoint = "https://secret@original.example" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example/path" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example?" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example?ssl=false" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example#fragment" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example:0" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example:65536" },
		func(c *connections.DatabendConfig) { c.Endpoint = "https://original.example:abc" },
		func(c *connections.DatabendConfig) { c.Auth = "guess" },
		func(c *connections.DatabendConfig) { c.Username = "" },
		func(c *connections.DatabendConfig) { c.Username = "one:two" },
		func(c *connections.DatabendConfig) { c.Auth = "bearer" },
		func(c *connections.DatabendConfig) { c.Auth = "none" },
		func(c *connections.DatabendConfig) { c.ServerName = "original.example\ninjected" },
		func(c *connections.DatabendConfig) { c.CAFile = "relative.pem" },
		func(c *connections.DatabendConfig) { c.ClientCertFile = filepath.Join(t.TempDir(), "client.pem") },
		func(c *connections.DatabendConfig) {
			c.Endpoint = "http://original.example"
			c.ServerName = "original.example"
		},
		func(c *connections.DatabendConfig) { c.ConnectTimeoutSeconds = -1 },
		func(c *connections.DatabendConfig) { c.RequestTimeoutSeconds = 3601 },
	} {
		c := base
		mutate(&c)
		if _, err := connections.NormalizeDatabendConfig(c); err == nil {
			t.Fatal("ambiguous original configuration accepted", c)
		}
	}
}
