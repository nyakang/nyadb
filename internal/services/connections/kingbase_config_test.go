package connections_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestKingbasePublicSettingsRoundTripWithoutAssumedDatabase(t *testing.T) {
	c := connections.KingbaseConfig{Host: " ::1 ", Database: "test", Username: "user"}
	got, err := connections.NormalizeKingbaseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "::1" || got.Port != 54321 || got.Database != "test" || got.TLSMode != "verify-full" || got.Options.ConnectTimeoutSeconds != 10 || c.Host != " ::1 " || c.Port != 0 {
		t.Fatal("public configuration defaults or immutable inputs changed")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "password") {
		t.Fatal("public settings contain a password field")
	}
	var round connections.KingbaseConfig
	if err = json.Unmarshal(b, &round); err != nil || round != got {
		t.Fatal("public settings did not round trip", err)
	}
}

func TestKingbaseRejectsAmbiguousOrUnsupportedSettings(t *testing.T) {
	base := connections.KingbaseConfig{Host: "localhost", Database: "test", Username: "user", Options: connections.DefaultPostgreSQLOptions()}
	for _, change := range []func(*connections.KingbaseConfig){
		func(c *connections.KingbaseConfig) { c.Host = "host:54321" },
		func(c *connections.KingbaseConfig) { c.Host = "kingbase://user@host" },
		func(c *connections.KingbaseConfig) { c.Host = "[::1]" },
		func(c *connections.KingbaseConfig) { c.Host = "a\x00b" },
		func(c *connections.KingbaseConfig) { c.Database = "" },
		func(c *connections.KingbaseConfig) { c.Username = "" },
		func(c *connections.KingbaseConfig) { c.TLSMode = "prefer" },
		func(c *connections.KingbaseConfig) { c.TLSMode = "disable"; c.Options.CAFile = "certificate.pem" },
		func(c *connections.KingbaseConfig) { c.Options.CertificateFile = "certificate.pem" },
		func(c *connections.KingbaseConfig) { c.Options.LockTimeoutMS = 1 },
		func(c *connections.KingbaseConfig) { c.Options.ToolsDirectory = "/tools" },
		func(c *connections.KingbaseConfig) { c.Options.SSH.Enabled = true },
		func(c *connections.KingbaseConfig) { c.Options.HTTP.Enabled = true },
	} {
		c := base
		change(&c)
		if _, err := connections.NormalizeKingbaseConfig(c); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("unsupported public settings accepted", err)
		}
	}
}
