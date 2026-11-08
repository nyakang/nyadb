package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CouchDBConfig contains public native HTTP settings. Credentials are supplied
// separately; this transport foundation does not register a public product.
type CouchDBConfig struct{ Endpoint, Auth, Username, CAFile, ServerName string }

func NormalizeCouchDBConfig(c CouchDBConfig) (CouchDBConfig, error) {
	for _, value := range []string{c.Endpoint, c.Auth, c.Username, c.CAFile, c.ServerName} {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.IndexFunc(c.Endpoint, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, ErrInvalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return c, ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\%") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	if strings.Contains(u.Path, "//") {
		return c, ErrInvalid
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	switch c.Auth {
	case "none":
		if c.Username != "" {
			return c, ErrInvalid
		}
	case "basic":
		// Basic uses ':' as a separator. Do not trim valid CouchDB principals.
		if c.Username == "" || strings.Contains(c.Username, ":") {
			return c, ErrInvalid
		}
	default:
		return c, ErrInvalid
	}
	if (u.Scheme == "http" && (c.CAFile != "" || c.ServerName != "")) || (c.CAFile != "" && !filepath.IsAbs(c.CAFile)) {
		return c, ErrInvalid
	}
	u.Path = strings.TrimRight(u.Path, "/")
	c.Endpoint = u.String()
	return c, nil
}

func ValidateCouchDBCredential(c CouchDBConfig, secret string, required bool) error {
	if (c.Auth != "none" && c.Auth != "basic") || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || (c.Auth == "none" && secret != "") || (required && c.Auth == "basic" && secret == "") {
		return ErrInvalid
	}
	return nil
}
