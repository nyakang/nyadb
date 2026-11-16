package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// DatabendConfig is the immutable HTTP target of an original Databend JDBC
// process. Credentials are supplied separately and never enter this value.
// This configuration alone does not publish a provider or catalog card.
type DatabendConfig struct {
	Endpoint, Auth, Username, CAFile, ServerName, ClientCertFile, ClientKeyFile string
	ConnectTimeoutSeconds, RequestTimeoutSeconds                                int
}

func NormalizeDatabendConfig(c DatabendConfig) (DatabendConfig, error) {
	for _, v := range []string{c.Endpoint, c.Auth, c.Username, c.CAFile, c.ServerName, c.ClientCertFile, c.ClientKeyFile} {
		if !validEngineText(v, 4096) {
			return c, ErrInvalid
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		strings.ContainsAny(u.Host, "\\ \t\r\n") || strings.Contains(u.Host, "%") {
		return c, ErrInvalid
	}
	if u.Port() != "" {
		if n, err := strconv.ParseUint(u.Port(), 10, 16); err != nil || n == 0 {
			return c, ErrInvalid
		}
	}
	if c.Auth == "" {
		c.Auth = "basic"
	}
	if c.Auth != "basic" && c.Auth != "bearer" && c.Auth != "none" ||
		c.Auth == "basic" && (c.Username == "" || strings.Contains(c.Username, ":")) ||
		c.Auth != "basic" && c.Username != "" {
		return c, ErrInvalid
	}
	if len(c.ServerName) > 253 || strings.IndexFunc(c.ServerName, unicode.IsSpace) >= 0 || strings.ContainsAny(c.ServerName, "/\\@?#[]") {
		return c, ErrInvalid
	}
	for _, path := range []string{c.CAFile, c.ClientCertFile, c.ClientKeyFile} {
		if path != "" && !filepath.IsAbs(path) {
			return c, ErrInvalid
		}
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") || u.Scheme == "http" && (c.CAFile != "" || c.ServerName != "" || c.ClientCertFile != "") {
		return c, ErrInvalid
	}
	if c.ConnectTimeoutSeconds == 0 {
		c.ConnectTimeoutSeconds = 15
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 120
	}
	if c.ConnectTimeoutSeconds < 1 || c.ConnectTimeoutSeconds > 3600 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 3600 {
		return c, ErrInvalid
	}
	u.Path = ""
	c.Endpoint = u.String()
	return c, nil
}
