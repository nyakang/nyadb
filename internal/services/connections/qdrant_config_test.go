package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQdrantConfigNativeEndpointAndSeparatedSecrets(t *testing.T) {
	base := connections.QdrantConfig{Endpoint: "https://[::1]:6333/proxy/", Auth: "api_key", CAFile: "/private/ca.crt", ServerName: "localhost", ClientCertFile: "/private/client.crt", ClientKeyFile: "/private/client.key"}
	c, err := connections.NormalizeQdrantConfig(base)
	if err != nil || c.Endpoint != "https://[::1]:6333/proxy" || c.ConnectTimeoutSeconds != 10 || c.RequestTimeoutSeconds != 40 {
		t.Fatal("native public settings", err)
	}
	for _, key := range []string{"exact-header-key", "token_1.~+/=", ""} {
		if err := connections.ValidateQdrantCredential(c, key, key != ""); err != nil {
			t.Fatal("supported API header", err)
		}
	}
	for _, key := range []string{" key", "key ", "key\nsecret", "密码", "key\x00", "key key"} {
		if err := connections.ValidateQdrantCredential(c, key, true); err == nil {
			t.Fatal("ambiguous credential serialization accepted")
		}
	}
	if connections.ValidateQdrantCredential(c, "", true) == nil || connections.ValidateQdrantCredential(connections.QdrantConfig{Auth: "none"}, "key", true) == nil {
		t.Fatal("missing or unexpected credential accepted")
	}
}

func TestQdrantConfigRejectsAmbiguousAddressAndTLS(t *testing.T) {
	base := connections.QdrantConfig{Endpoint: "http://localhost:6333", Auth: "none"}
	mutations := []func(*connections.QdrantConfig){
		func(c *connections.QdrantConfig) { c.Endpoint = "http://user:secret@localhost:6333" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost:6333?api-key=secret" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost:6333/#fragment" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost:0" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost:" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost:65536" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost/a/../b" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost/a//b" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost/%2f" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost/ prefix" },
		func(c *connections.QdrantConfig) { c.Auth = "basic" },
		func(c *connections.QdrantConfig) { c.CAFile = "/private/ca" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost"; c.CAFile = "relative/ca" },
		func(c *connections.QdrantConfig) { c.Endpoint = "https://localhost"; c.ClientCertFile = "/cert" },
		func(c *connections.QdrantConfig) { c.ConnectTimeoutSeconds = 121 },
		func(c *connections.QdrantConfig) { c.RequestTimeoutSeconds = -1 },
	}
	for i, mutate := range mutations {
		c := base
		mutate(&c)
		if _, err := connections.NormalizeQdrantConfig(c); err == nil {
			t.Fatalf("invalid public configuration accepted: %d", i)
		}
	}
}
