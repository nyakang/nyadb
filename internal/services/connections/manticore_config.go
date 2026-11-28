package connections

import (
	"net"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ManticoreConfig is the private native endpoint configuration. It deliberately
// does not inherit MySQL catalogs, session initialization, TLS or transactions.
// Public profiles, URI imports and vault publication require separate acceptance.
type ManticoreConfig struct {
	Host                  string `json:"host"`
	Port                  uint16 `json:"port"`
	Username              string `json:"username"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	QueryTimeoutSeconds   int    `json:"query_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	SQLMaxConnections     int    `json:"sql_max_connections"`
}

func NormalizeManticoreConfig(c ManticoreConfig) (ManticoreConfig, error) {
	if c.Port == 0 {
		c.Port = 9306
	}
	if c.ConnectTimeoutSeconds == 0 {
		c.ConnectTimeoutSeconds = 10
	}
	if c.QueryTimeoutSeconds == 0 {
		c.QueryTimeoutSeconds = 60
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 4
	}
	if c.SQLMaxConnections == 0 {
		c.SQLMaxConnections = 4
	}
	if c.Host == "" || len(c.Host) > 253 || !utf8.ValidString(c.Host) || strings.IndexFunc(c.Host, unicode.IsSpace) >= 0 || strings.IndexFunc(c.Host, unicode.IsControl) >= 0 || strings.ContainsAny(c.Host, "@/?!#\\") || strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil || len(c.Username) > 256 || !utf8.ValidString(c.Username) || strings.IndexFunc(c.Username, unicode.IsControl) >= 0 || c.ConnectTimeoutSeconds < 1 || c.ConnectTimeoutSeconds > 300 || c.QueryTimeoutSeconds < 1 || c.QueryTimeoutSeconds > 3600 || c.MaxConnections < 1 || c.MaxConnections > 64 || c.SQLMaxConnections < 1 || c.SQLMaxConnections > 32 {
		return c, ErrInvalid
	}
	return c, nil
}
