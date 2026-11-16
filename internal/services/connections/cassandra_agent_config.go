package connections

import (
	"net"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CassandraAgentConfig is a non-secret native connection snapshot. Public profile
// publication is a separate product acceptance step.
type CassandraAgentConfig struct {
	ContactPoints                       []string
	Port                                int
	ProtocolVersion                     int
	Keyspace, Username, LocalDatacenter string
	ConnectTimeoutMS, RequestTimeoutMS  int
}

func NormalizeCassandraAgentConfig(c CassandraAgentConfig) (CassandraAgentConfig, error) {
	c.ContactPoints = append([]string(nil), c.ContactPoints...)
	if len(c.ContactPoints) == 0 || len(c.ContactPoints) > 64 {
		return c, ErrInvalid
	}
	seen := map[string]bool{}
	for i, host := range c.ContactPoints {
		if strings.IndexFunc(host, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
		host = strings.TrimSpace(host)
		if host == "" || len(host) > 253 || !utf8.ValidString(host) || strings.ContainsAny(host, ",/@?#[]\\\x00\r\n\t ") || strings.Contains(host, ":") && net.ParseIP(host) == nil || seen[host] {
			return c, ErrInvalid
		}
		seen[host] = true
		c.ContactPoints[i] = host
	}
	if c.Port == 0 {
		c.Port = 9042
	}
	if c.ConnectTimeoutMS == 0 {
		c.ConnectTimeoutMS = 15000
	}
	if c.RequestTimeoutMS == 0 {
		c.RequestTimeoutMS = 11000
	}
	if c.Port < 1 || c.Port > 65535 || c.ProtocolVersion != 0 && (c.ProtocolVersion < 3 || c.ProtocolVersion > 5) || c.ConnectTimeoutMS < 250 || c.ConnectTimeoutMS > 300000 || c.RequestTimeoutMS < 250 || c.RequestTimeoutMS > 300000 || !ValidCassandraName(c.Keyspace, true) || !ValidCassandraName(c.Username, true) || !ValidCassandraName(c.LocalDatacenter, true) {
		return c, ErrInvalid
	}
	return c, nil
}

func ValidCassandraName(v string, empty bool) bool {
	return (empty || v != "") && len(v) <= 1024 && utf8.ValidString(v) && !strings.ContainsAny(v, "\x00\r\n")
}
