package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTrinoConfigKeepsHTTPOriginAndSecretsSeparate(t *testing.T) {
	c, err := connections.NormalizeTrinoConfig(connections.TrinoConfig{Endpoint: "https://localhost:8443/", Username: "user", Catalog: "memory", Schema: "default"})
	if err != nil || c.Endpoint != "https://localhost:8443" {
		t.Fatal("origin normalization failed", err)
	}
	for _, endpoint := range []string{"https://user:password@localhost", "http://localhost/v1/statement", "https://localhost?token=secret", "https://localhost#fragment", "file:///tmp/db", "https://localhost/%2e", "https://localhost:bad"} {
		c.Endpoint = endpoint
		if _, err = connections.NormalizeTrinoConfig(c); err == nil {
			t.Fatal("invalid or credential-bearing endpoint accepted", endpoint)
		}
	}
	c.Endpoint = "http://localhost:8080"
	c.CAFile = "/tmp/ca.pem"
	if _, err = connections.NormalizeTrinoConfig(c); err == nil {
		t.Fatal("plaintext silently ignored TLS settings")
	}
}
