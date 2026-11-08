package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// QdrantConfig is public native REST configuration, with no credential. The
// transport foundation does not register a product or a workbench provider.
type QdrantConfig struct {
	Endpoint, Auth, CAFile, ServerName, ClientCertFile, ClientKeyFile string
	ConnectTimeoutSeconds, RequestTimeoutSeconds                      int
}

func NormalizeQdrantConfig(c QdrantConfig) (QdrantConfig, error) {
	for _, value := range []string{c.Endpoint, c.Auth, c.CAFile, c.ServerName, c.ClientCertFile, c.ClientKeyFile} {
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
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\%") {
			return c, ErrInvalid
		}
	}
	if strings.Contains(u.Path, "//") || strings.IndexFunc(c.ServerName, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	if c.Auth != "none" && c.Auth != "api_key" {
		return c, ErrInvalid
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") || u.Scheme == "http" && (c.CAFile != "" || c.ServerName != "" || c.ClientCertFile != "") {
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

// Header bytes must survive HTTP serialization unchanged. No trimming, Basic
// fallback or environment credential lookup is performed.
func ValidateQdrantCredential(c QdrantConfig, secret string, required bool) error {
	if c.Auth != "none" && c.Auth != "api_key" || len(secret) > 4096 || c.Auth == "none" && secret != "" || required && c.Auth == "api_key" && secret == "" {
		return ErrInvalid
	}
	for _, ch := range secret {
		if ch < 33 || ch > 126 {
			return ErrInvalid
		}
	}
	return nil
}
