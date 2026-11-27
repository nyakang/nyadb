package connections_test

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func prestoConfig() connections.PrestoSQLManagedConfig {
	return connections.PrestoSQLManagedConfig{Endpoint: "http://[::1]", Username: "user", Catalog: "memory", Schema: "default",
		DriverVersion: "jdbc-350-plugin-0.1.57", DriverSHA256: strings.Repeat("a", 64)}
}

func TestPrivatePrestoSQLConfigPreservesProductEndpointAndRejectsStoredCredentials(t *testing.T) {
	c, err := connections.NormalizePrestoSQLManagedConfig(prestoConfig())
	if err != nil || c.Endpoint != "http://[::1]:8080" || c.ConnectTimeoutSeconds != 30 || c.MaxQueryProcesses != 4 {
		t.Fatal(c, err)
	}
	for _, params := range []string{"password=secret", "user=other", "accessToken=secret", "SSLTrustStorePassword=secret", "SSL=true", "source=one&source=two", "source=bad%0Avalue", "unknown=x", "source=%", "source=prefix%26SSL%3Dtrue"} {
		candidate := prestoConfig()
		candidate.JDBCParameters = params
		if _, err := connections.NormalizePrestoSQLManagedConfig(candidate); err == nil {
			t.Fatal("ambiguous/secret/foreign driver properties persisted", params)
		}
	}
	for _, endpoint := range []string{"http://user:password@host", "http://host?password=x", "http://host?", "http://host/path", "http://host:0", "http://host:65536", "http://host:", "file:///tmp/foreign", "jdbc:presto://host"} {
		candidate := prestoConfig()
		candidate.Endpoint = endpoint
		if _, err := connections.NormalizePrestoSQLManagedConfig(candidate); err == nil {
			t.Fatal("unqualified/credential-bearing endpoint admitted", endpoint)
		}
	}
	for _, mutate := range []func(*connections.PrestoSQLManagedConfig){
		func(c *connections.PrestoSQLManagedConfig) { c.Username = "" },
		func(c *connections.PrestoSQLManagedConfig) { c.Catalog = "" },
		func(c *connections.PrestoSQLManagedConfig) { c.Schema = "default/other" },
		func(c *connections.PrestoSQLManagedConfig) { c.DriverVersion = "../foreign" },
		func(c *connections.PrestoSQLManagedConfig) { c.DriverSHA256 = strings.Repeat("A", 64) },
		func(c *connections.PrestoSQLManagedConfig) { c.ConnectTimeoutSeconds = 301 },
		func(c *connections.PrestoSQLManagedConfig) { c.QueryTimeoutSeconds = 3601 },
		func(c *connections.PrestoSQLManagedConfig) { c.MaxQueryProcesses = 17 },
		func(c *connections.PrestoSQLManagedConfig) { c.PoolWaitMillis = 1 },
	} {
		candidate := prestoConfig()
		mutate(&candidate)
		if _, err := connections.NormalizePrestoSQLManagedConfig(candidate); err == nil {
			t.Fatal("invalid private config accepted", candidate)
		}
	}
}
