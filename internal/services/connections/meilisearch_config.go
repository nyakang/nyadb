package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MeilisearchConfig holds public native HTTP settings, separately from the API
// key. This foundation does not register a public connection product.
type MeilisearchConfig struct {
	Endpoint, Auth, CAFile, ServerName, ClientCertFile, ClientKeyFile string
	// IndexUID explicitly selects an index-scoped handshake. Such native keys
	// cannot have global version permission, so binary identity is unavailable.
	IndexUID                                     string
	ConnectTimeoutSeconds, RequestTimeoutSeconds int
}

func NormalizeMeilisearchConfig(c MeilisearchConfig) (MeilisearchConfig, error) {
	for _, value := range []string{c.Endpoint, c.Auth, c.CAFile, c.ServerName, c.ClientCertFile, c.ClientKeyFile, c.IndexUID} {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	if c.IndexUID != "" && !IsMeilisearchIndexUID(c.IndexUID) {
		return c, ErrInvalid
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
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\%") || strings.IndexFunc(segment, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	if strings.Contains(u.Path, "//") || strings.IndexFunc(c.ServerName, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	if c.Auth != "none" && c.Auth != "bearer" {
		return c, ErrInvalid
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") || (u.Scheme == "http" && (c.CAFile != "" || c.ServerName != "" || c.ClientCertFile != "")) {
		return c, ErrInvalid
	}
	for _, path := range []string{c.CAFile, c.ClientCertFile, c.ClientKeyFile} {
		if path != "" && !filepath.IsAbs(path) {
			return c, ErrInvalid
		}
	}
	if c.ConnectTimeoutSeconds == 0 {
		c.ConnectTimeoutSeconds = 10
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 40
	}
	if c.ConnectTimeoutSeconds < 1 || c.ConnectTimeoutSeconds > 120 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 600 {
		return c, ErrInvalid
	}
	u.Path = strings.TrimRight(u.Path, "/")
	c.Endpoint = u.String()
	return c, nil
}

func IsMeilisearchIndexUID(uid string) bool {
	if len(uid) == 0 || len(uid) > 512 {
		return false
	}
	for _, ch := range uid {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// Bearer secrets use the HTTP token grammar, without whitespace normalization.
// Both API keys and tenant tokens retain their original bytes; the server still
// decides their permissions. No secret is carried in the public configuration.
func ValidateMeilisearchCredential(c MeilisearchConfig, secret string, required bool) error {
	if c.Auth != "none" && c.Auth != "bearer" || len(secret) > 4096 || c.Auth == "none" && secret != "" || required && c.Auth == "bearer" && secret == "" {
		return ErrInvalid
	}
	padding := false
	for _, ch := range secret {
		if ch == '=' {
			padding = true
			continue
		}
		if padding || !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~+/", ch)) {
			return ErrInvalid
		}
	}
	if secret != "" && strings.TrimRight(secret, "=") == "" {
		return ErrInvalid
	}
	return nil
}
