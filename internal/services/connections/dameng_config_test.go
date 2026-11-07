package connections_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func damengConfig(t *testing.T) connections.DamengConfig {
	t.Helper()
	a := connections.DefaultDamengOptions()
	a.LibraryFile = filepath.Join(t.TempDir(), "libdmdpi.so")
	return connections.DamengConfig{Host: "localhost", Database: "DAMENG", Username: "SYSDBA", TLSMode: "disable", Options: a}
}

func TestDamengExplicitPublicSettingsRoundTrip(t *testing.T) {
	c := damengConfig(t)
	c.Host = " ::1 "
	c.Options.DefaultSchema = "租户 ' \" 名"
	before := c
	got, err := connections.NormalizeDamengConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "::1" || got.Port != 5236 || got.Database != "DAMENG" || got.Options.DefaultSchema != c.Options.DefaultSchema || c != before {
		t.Fatal("explicit native settings/defaults or input ownership changed")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "secret") {
		t.Fatal("public configuration contains credential fields")
	}
	var round connections.DamengConfig
	if err = json.Unmarshal(data, &round); err != nil || round != got {
		t.Fatal("public settings did not round trip", err)
	}
	c.TLSMode = " VERIFY-FULL "
	c.Options.CAFile = filepath.Join(t.TempDir(), "ca-cert.pem")
	c.Options.CertificateFile = filepath.Join(t.TempDir(), "client-cert.pem")
	c.Options.KeyFile = filepath.Join(t.TempDir(), "client-key.pem")
	c.Options.ServerName = "database.example"
	got, err = connections.NormalizeDamengConfig(c)
	if err != nil || got.TLSMode != "verify-full" {
		t.Fatal("explicit certificate identity was not retained", err)
	}
	c.TLSMode = ""
	got, err = connections.NormalizeDamengConfig(c)
	if err != nil || got.TLSMode != "verify-full" {
		t.Fatal("missing TLS mode did not require full identity verification", err)
	}
}

func TestDamengRejectsAmbiguousOrUnsupportedPublicSettings(t *testing.T) {
	base := damengConfig(t)
	for _, change := range []func(*connections.DamengConfig){
		func(c *connections.DamengConfig) { c.Host = "" },
		func(c *connections.DamengConfig) { c.Host = "dm://user:secret@localhost" },
		func(c *connections.DamengConfig) { c.Host = "localhost:5236" },
		func(c *connections.DamengConfig) { c.Host = "[::1]" },
		func(c *connections.DamengConfig) { c.Host = "one,two" },
		func(c *connections.DamengConfig) { c.Host = "bad\x00name" },
		func(c *connections.DamengConfig) { c.Database = "" },
		func(c *connections.DamengConfig) { c.Username = "" },
		func(c *connections.DamengConfig) { c.TLSMode = "prefer" },
		func(c *connections.DamengConfig) { c.TLSMode = "verify-full" },
		func(c *connections.DamengConfig) { c.Options.Version = 2 },
		func(c *connections.DamengConfig) { c.Options.LibraryFile = "relative.so" },
		func(c *connections.DamengConfig) { c.Options.LibraryFile = "" },
		func(c *connections.DamengConfig) { c.Options.CAFile = filepath.Join(t.TempDir(), "ca.pem") },
		func(c *connections.DamengConfig) { c.Options.CertificateFile = filepath.Join(t.TempDir(), "cert.pem") },
		func(c *connections.DamengConfig) { c.Options.DefaultSchema = "bad\nname" },
		func(c *connections.DamengConfig) { c.Options.ServerName = "bad/name" },
		func(c *connections.DamengConfig) { c.Options.ConnectTimeoutSeconds = 0 },
		func(c *connections.DamengConfig) { c.Options.ConnectTimeoutSeconds = 121 },
		func(c *connections.DamengConfig) { c.Options.OperationTimeoutSeconds = -1 },
		func(c *connections.DamengConfig) { c.Options.MaxConnections = 0 },
		func(c *connections.DamengConfig) { c.Options.SQLMaxConnections = 65 },
	} {
		c := base
		change(&c)
		if _, err := connections.NormalizeDamengConfig(c); !errors.Is(err, connections.ErrInvalid) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid settings accepted or echoed: %v", err)
		}
	}
}
