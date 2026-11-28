package connections_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestManticorePrivateEndpointValidation(t *testing.T) {
	c, e := connections.NormalizeManticoreConfig(connections.ManticoreConfig{Host: "::1", Username: "root"})
	if e != nil || c.Port != 9306 || c.SQLMaxConnections != 4 {
		t.Fatalf("defaults: %+v %v", c, e)
	}
	for _, host := range []string{"", "user@host", "localhost/database", "localhost?tls=true", "localhost:9306", "[::1]", "host\x00", "a b", "a\\b"} {
		if _, e := connections.NormalizeManticoreConfig(connections.ManticoreConfig{Host: host}); !errors.Is(e, connections.ErrInvalid) {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, mutate := range []func(*connections.ManticoreConfig){func(c *connections.ManticoreConfig) { c.Username = "a\n" }, func(c *connections.ManticoreConfig) { c.QueryTimeoutSeconds = -1 }, func(c *connections.ManticoreConfig) { c.SQLMaxConnections = 33 }, func(c *connections.ManticoreConfig) { c.MaxConnections = 65 }, func(c *connections.ManticoreConfig) { c.ConnectTimeoutSeconds = 301 }} {
		next := c
		mutate(&next)
		if _, e := connections.NormalizeManticoreConfig(next); !errors.Is(e, connections.ErrInvalid) {
			t.Fatalf("accepted %+v", next)
		}
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "password") {
		t.Fatal("credentials in config")
	}
}
