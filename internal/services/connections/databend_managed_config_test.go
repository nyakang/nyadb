package connections_test

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDatabendManagedSelectionRetainsOriginalTargetAndPoolCapacity(t *testing.T) {
	c := connections.DatabendManagedConfig{Transport: connections.DatabendConfig{Endpoint: "https://original.example", Username: "root"}, Database: "default", DriverVersion: "jdbc-0.4.8-agent-0.1.82", DriverSHA256: strings.Repeat("a", 64)}
	n, err := connections.NormalizeDatabendManagedConfig(c)
	if err != nil || n.Database != c.Database || n.DriverVersion != c.DriverVersion || n.DriverSHA256 != c.DriverSHA256 || n.MaxConnections != 4 || n.MetadataReserve != 1 || n.PoolWaitMillis != 5000 {
		t.Fatal("original immutable selection changed", err)
	}
	for _, change := range []func(*connections.DatabendManagedConfig){
		func(c *connections.DatabendManagedConfig) { c.DriverVersion = "../replacement" },
		func(c *connections.DatabendManagedConfig) { c.DriverSHA256 = strings.Repeat("A", 64) },
		func(c *connections.DatabendManagedConfig) { c.DriverSHA256 = "implicit-latest" },
		func(c *connections.DatabendManagedConfig) { c.Database = "" },
		func(c *connections.DatabendManagedConfig) { c.Database = "default\nforeign" },
		func(c *connections.DatabendManagedConfig) { c.MaxConnections = 1 },
		func(c *connections.DatabendManagedConfig) { c.MaxConnections = 33 },
		func(c *connections.DatabendManagedConfig) { c.MetadataReserve = 4 },
		func(c *connections.DatabendManagedConfig) { c.PoolWaitMillis = 249 },
	} {
		invalid := c
		change(&invalid)
		if _, err := connections.NormalizeDatabendManagedConfig(invalid); err == nil {
			t.Fatal("ambiguous driver selection or pool capacity accepted", invalid)
		}
	}
}
