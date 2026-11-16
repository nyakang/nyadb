package connections

import (
	"encoding/hex"
	"regexp"
	"strings"
)

// DatabendManagedConfig selects one immutable original driver and its HTTP
// target. It contains no secrets and does not publish a Profile/provider.
type DatabendManagedConfig struct {
	Transport                                       DatabendConfig
	Database, DriverVersion, DriverSHA256           string
	MaxConnections, MetadataReserve, PoolWaitMillis int
}

var databendDriverVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func NormalizeDatabendManagedConfig(c DatabendManagedConfig) (DatabendManagedConfig, error) {
	var err error
	c.Transport, err = NormalizeDatabendConfig(c.Transport)
	if err != nil || c.Database == "" || !validEngineText(c.Database, 256) || !databendDriverVersion.MatchString(c.DriverVersion) {
		return c, ErrInvalid
	}
	if _, err := hex.DecodeString(c.DriverSHA256); err != nil || len(c.DriverSHA256) != 64 || strings.ToLower(c.DriverSHA256) != c.DriverSHA256 {
		return c, ErrInvalid
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 4
	}
	if c.MetadataReserve == 0 {
		c.MetadataReserve = 1
	}
	if c.PoolWaitMillis == 0 {
		c.PoolWaitMillis = 5000
	}
	if c.MaxConnections < 2 || c.MaxConnections > 32 || c.MetadataReserve < 1 || c.MetadataReserve >= c.MaxConnections || c.PoolWaitMillis < 250 || c.PoolWaitMillis > 300000 {
		return c, ErrInvalid
	}
	return c, nil
}
