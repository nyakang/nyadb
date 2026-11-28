package connections

import (
	"encoding/hex"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PrestoSQLManagedConfig is the immutable adapter configuration derived from
// a public profile and its explicitly selected managed driver package.
// JDBCParameters contains only the original driver's non-secret properties.
type PrestoSQLManagedConfig struct {
	Endpoint, Username, Catalog, Schema         string
	JDBCParameters, DriverVersion, DriverSHA256 string
	ConnectTimeoutSeconds, QueryTimeoutSeconds  int
	MaxQueryProcesses, PoolWaitMillis           int
}

var prestoDriverVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// PrestoSQLSecretParameter identifies ephemeral original JDBC350 properties.
// Credentials must not be encoded in the persisted URL/parameter string.
func PrestoSQLSecretParameter(name string) bool {
	switch name {
	case "SSLKeyStorePassword", "SSLTrustStorePassword", "accessToken", "extraCredentials":
		return true
	}
	return false
}

func prestoPublicParameter(name string) bool {
	switch name {
	case "roles", "socksProxy", "httpProxy", "applicationNamePrefix", "disableCompression",
		"SSLVerification", "SSLKeyStorePath", "SSLKeyStoreType", "SSLTrustStorePath", "SSLTrustStoreType",
		"KerberosServicePrincipalPattern", "KerberosRemoteServiceName", "KerberosUseCanonicalHostname",
		"KerberosPrincipal", "KerberosConfigPath", "KerberosKeytabPath", "KerberosCredentialCachePath",
		"clientInfo", "clientTags", "traceToken", "sessionProperties", "source":
		return true
	}
	return false
}

func prestoConfigText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func NormalizePrestoSQLManagedConfig(c PrestoSQLManagedConfig) (PrestoSQLManagedConfig, error) {
	for _, value := range []string{c.Endpoint, c.Username, c.Catalog, c.Schema} {
		if !prestoConfigText(value, 4096) {
			return c, ErrInvalid
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" || u.Scheme != "http" && u.Scheme != "https" {
		return c, ErrInvalid
	}
	if u.Port() == "" {
		if strings.HasSuffix(u.Host, ":") {
			return c, ErrInvalid
		}
		u.Host = net.JoinHostPort(u.Hostname(), "8080")
	} else if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
		return c, ErrInvalid
	}
	if c.Username == "" || len(c.Catalog) > 256 || len(c.Schema) > 256 || strings.ContainsAny(c.Catalog+c.Schema, "/\\") || c.Schema != "" && c.Catalog == "" || !prestoDriverVersion.MatchString(c.DriverVersion) {
		return c, ErrInvalid
	}
	if _, err = hex.DecodeString(c.DriverSHA256); err != nil || len(c.DriverSHA256) != 64 || strings.ToLower(c.DriverSHA256) != c.DriverSHA256 {
		return c, ErrInvalid
	}
	if len(c.JDBCParameters) > 16384 {
		return c, ErrInvalid
	}
	params, err := url.ParseQuery(c.JDBCParameters)
	if err != nil || len(params) > 32 {
		return c, ErrInvalid
	}
	for name, values := range params {
		if !prestoPublicParameter(name) || len(values) != 1 || !prestoConfigText(values[0], 4096) || strings.ContainsRune(values[0], '&') {
			return c, ErrInvalid
		}
	}
	if c.ConnectTimeoutSeconds == 0 {
		c.ConnectTimeoutSeconds = 30
	}
	if c.MaxQueryProcesses == 0 {
		c.MaxQueryProcesses = 4
	}
	if c.PoolWaitMillis == 0 {
		c.PoolWaitMillis = 5000
	}
	if c.ConnectTimeoutSeconds < 1 || c.ConnectTimeoutSeconds > 300 || c.QueryTimeoutSeconds < 0 || c.QueryTimeoutSeconds > 3600 || c.MaxQueryProcesses < 1 || c.MaxQueryProcesses > 16 || c.PoolWaitMillis < 250 || c.PoolWaitMillis > 300000 {
		return c, ErrInvalid
	}
	u.Path = ""
	c.Endpoint, c.JDBCParameters = u.String(), params.Encode()
	return c, nil
}
