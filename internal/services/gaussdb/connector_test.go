package gaussdb

import (
	"crypto/tls"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func testConfig() connections.GaussDBConfig {
	return connections.GaussDBConfig{Endpoints: []connections.GaussDBEndpoint{{Host: "first.example", Port: 5432}, {Host: "second.example", Port: 6432}, {Host: "2001:db8::1", Port: 7432}}, Database: `fixed ' db`, Username: `reader ' name`, Options: connections.DefaultPostgreSQLOptions()}
}

func TestNativeConfigKeepsExplicitCredentialsRuntimeSettingsAndEndpointTLS(t *testing.T) {
	for key, value := range map[string]string{"PGPASSWORD": "environment-password", "PGHOST": "foreign.example", "PGPORT": "9999", "PGDATABASE": "foreign-db", "PGUSER": "foreign-user", "PGSSLMODE": "disable", "PGSSLCERT": "/missing/environment.crt", "PGSSLKEY": "/missing/environment.key", "PGSSLROOTCERT": "/missing/environment-ca.crt", "PGTARGETSESSIONATTRS": "read-only", "PGOPTIONS": "foreign-runtime-settings"} {
		t.Setenv(key, value)
	}
	c := testConfig()
	c.Options.ApplicationName, c.Options.DefaultSchema, c.Options.StatementTimeoutMS = "native settings", `a"b`, 1234
	cfg, err := nativeConfig(c, `private ' password`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != `private ' password` || cfg.User != c.Username || cfg.Database != c.Database || cfg.Tracer != nil || cfg.ValidateConnect != nil || strings.Contains(cfg.ConnString(), "private") || strings.Contains(cfg.ConnString(), "fixed") {
		t.Fatal("explicit configuration was replaced or exposed by driver defaults")
	}
	if cfg.RuntimeParams["search_path"] != `"a""b"` || cfg.RuntimeParams["application_name"] != "native settings" || cfg.RuntimeParams["statement_timeout"] != "1234" || len(cfg.RuntimeParams) != 3 {
		t.Fatal("native runtime settings contain foreign options")
	}
	if len(cfg.Fallbacks) != 2 || cfg.Host != "first.example" || cfg.Port != 5432 {
		t.Fatal("endpoint order or ports lost")
	}
	for i, endpoint := range c.Endpoints {
		tlsConfig := cfg.TLSConfig
		if i > 0 {
			fallback := cfg.Fallbacks[i-1]
			if fallback.Host != endpoint.Host || fallback.Port != endpoint.Port {
				t.Fatal("fallback endpoint identity changed")
			}
			tlsConfig = fallback.TLSConfig
		}
		if tlsConfig == nil || tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != endpoint.Host || tlsConfig.MinVersion < tls.VersionTLS12 {
			t.Fatal("verified endpoint TLS downgraded")
		}
	}
	cfg.TLSConfig.ServerName = "changed"
	if cfg.Fallbacks[0].TLSConfig.ServerName != "second.example" {
		t.Fatal("endpoint TLS configs are aliased")
	}
	if cfg, err = nativeConfig(c, "", nil); err != nil || cfg.Password != "" {
		t.Fatal("environment password used for an empty explicit password", err)
	}
}

func TestNativeConfigDowngradesOnlyWhenExplicitlyPreferred(t *testing.T) {
	for _, mode := range []string{"disable", "prefer", "require", "verify-ca", "verify-full"} {
		c := testConfig()
		c.TLSMode = mode
		cfg, err := nativeConfig(c, "", nil)
		if err != nil {
			t.Fatal(mode, err)
		}
		if mode == "prefer" {
			if len(cfg.Fallbacks) != 5 || cfg.Fallbacks[0].TLSConfig != nil || cfg.Fallbacks[1].TLSConfig == nil || cfg.Fallbacks[2].TLSConfig != nil {
				t.Fatal("prefer did not retain endpoint-local TLS/plaintext ordering")
			}
		} else if len(cfg.Fallbacks) != 2 {
			t.Fatal("unexpected endpoint fallbacks", mode)
		}
		if (cfg.TLSConfig == nil) != (mode == "disable") {
			t.Fatal("TLS mode ignored", mode)
		}
		if mode != "disable" && mode != "prefer" {
			for _, f := range cfg.Fallbacks {
				if f.TLSConfig == nil {
					t.Fatal("mandatory TLS permits plaintext fallback", mode)
				}
			}
		}
	}
}
