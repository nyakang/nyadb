package connections_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMeilisearchConfigEndpointTLSAndSeparateBearer(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:7700/", "https://localhost:7700/proxy/meili/", "http://[::1]:7700/"} {
		c, err := connections.NormalizeMeilisearchConfig(connections.MeilisearchConfig{Endpoint: endpoint})
		if err != nil || c.Endpoint != strings.TrimRight(endpoint, "/") || c.Auth != "none" || c.ConnectTimeoutSeconds != 10 || c.RequestTimeoutSeconds != 40 {
			t.Fatal("endpoint or defaults changed", err)
		}
	}
	base := connections.MeilisearchConfig{Endpoint: "https://localhost:7700", Auth: "bearer", CAFile: filepath.Join(t.TempDir(), "ca.pem")}
	for _, secret := range []string{"owned-key_1", "eyJhbGci.payload.signature", "a+/~==", ""} {
		if err := connections.ValidateMeilisearchCredential(base, secret, secret != ""); err != nil {
			t.Fatal("valid separate Bearer rejected", err)
		}
	}
	for _, secret := range []string{"", " padded", "padded ", "a\nb", "a=b", "==", "用户", string([]byte{0xff}), strings.Repeat("a", 4097)} {
		if !errors.Is(connections.ValidateMeilisearchCredential(base, secret, true), connections.ErrInvalid) {
			t.Fatal("invalid Bearer accepted")
		}
	}
	if connections.ValidateMeilisearchCredential(connections.MeilisearchConfig{Auth: "none"}, "stray", false) == nil {
		t.Fatal("anonymous config retained secret")
	}
	for _, change := range []func(*connections.MeilisearchConfig){
		func(c *connections.MeilisearchConfig) { c.Endpoint = "http://user:key@localhost:7700" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "?key=secret" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "?" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "#fragment" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "/../other" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "/%2e%2e/other" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "/%252e/other" },
		func(c *connections.MeilisearchConfig) { c.Endpoint += "//other" },
		func(c *connections.MeilisearchConfig) { c.Endpoint = "https://localhost:0" },
		func(c *connections.MeilisearchConfig) { c.Endpoint = "https://localhost:" },
		func(c *connections.MeilisearchConfig) { c.Endpoint = "https://localhost:65536" },
		func(c *connections.MeilisearchConfig) { c.Endpoint = "file:///tmp/meili" },
		func(c *connections.MeilisearchConfig) { c.Endpoint = "http://localhost:7700" },
		func(c *connections.MeilisearchConfig) { c.CAFile = "relative.pem" },
		func(c *connections.MeilisearchConfig) { c.ServerName = "bad name" },
		func(c *connections.MeilisearchConfig) { c.ClientCertFile = filepath.Join(t.TempDir(), "client.pem") },
		func(c *connections.MeilisearchConfig) { c.Auth = "basic" },
		func(c *connections.MeilisearchConfig) { c.IndexUID = "../index" },
		func(c *connections.MeilisearchConfig) { c.ConnectTimeoutSeconds = -1 },
		func(c *connections.MeilisearchConfig) { c.RequestTimeoutSeconds = 601 },
	} {
		c := base
		change(&c)
		if _, err := connections.NormalizeMeilisearchConfig(c); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("ambiguous native config accepted")
		}
	}
}
