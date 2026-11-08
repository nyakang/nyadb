package connections

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CassandraConfig holds public native transport settings. Credentials are supplied
// separately; this foundation does not register a public connection product.
type CassandraConfig struct{ Endpoint, Auth, Username, TLSMode, CAFile, ServerName, Keyspace string }

func NormalizeCassandraConfig(c CassandraConfig) (CassandraConfig, error) {
	for _, s := range []string{c.Endpoint, c.Auth, c.Username, c.TLSMode, c.CAFile, c.ServerName, c.Keyspace} {
		if len(s) > 4096 || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	host, port, err := net.SplitHostPort(c.Endpoint)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || host == "" || n < 1 || n > 65535 || strings.ContainsAny(host, "/@?#%\\") || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	c.Endpoint = net.JoinHostPort(host, strconv.Itoa(n))
	if len(c.Keyspace) > 48 || strings.IndexFunc(c.Keyspace, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) >= 0 {
		return c, ErrInvalid
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	if c.TLSMode == "" {
		c.TLSMode = "verify-full"
	}
	if c.Auth != "none" && c.Auth != "password" || c.Auth == "none" && c.Username != "" || c.Auth == "password" && c.Username == "" {
		return c, ErrInvalid
	}
	if c.TLSMode != "disable" && c.TLSMode != "verify-full" || c.TLSMode == "disable" && (c.CAFile != "" || c.ServerName != "") || c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		return c, ErrInvalid
	}
	return c, nil
}
func ValidateCassandraCredential(c CassandraConfig, secret string, required bool) error {
	if c.Auth != "none" && c.Auth != "password" || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || c.Auth == "none" && secret != "" || required && c.Auth == "password" && secret == "" {
		return ErrInvalid
	}
	return nil
}
