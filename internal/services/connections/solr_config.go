package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SolrConfig contains native public transport settings. The Basic password is
// supplied separately. Profile persistence and public registration are separate.
type SolrConfig struct{ Endpoint, Auth, Username, CAFile, ServerName string }

func NormalizeSolrConfig(c SolrConfig) (SolrConfig, error) {
	for _, s := range []string{c.Endpoint, c.Auth, c.Username, c.CAFile, c.ServerName} {
		if len(s) > 4096 || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.IndexFunc(c.Endpoint, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if port := u.Port(); port != "" {
		value, e := strconv.Atoi(port)
		if e != nil || value < 1 || value > 65535 {
			return c, ErrInvalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return c, ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.ContainsAny(part, "\\%") {
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
		if c.Username == "" || strings.Contains(c.Username, ":") || solrTrimPadding(c.Username) {
			return c, ErrInvalid
		}
	default:
		return c, ErrInvalid
	}
	if (u.Scheme == "http" && (c.Auth != "none" || c.CAFile != "" || c.ServerName != "")) || (c.CAFile != "" && !filepath.IsAbs(c.CAFile)) {
		return c, ErrInvalid
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/solr"
	}
	c.Endpoint = u.String()
	return c, nil
}
func ValidateSolrCredential(c SolrConfig, secret string, required bool) error {
	if (c.Auth != "none" && c.Auth != "basic") || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || (c.Auth == "none" && secret != "") || (required && c.Auth == "basic" && secret == "") {
		return ErrInvalid
	}
	if c.Auth == "basic" && solrTrimPadding(secret) {
		return ErrInvalid
	}
	return nil
}

// The original BasicAuthPlugin uses Java String.trim on both username and
// password. Reject padding it would silently remove; retain other Unicode.
func solrTrimPadding(s string) bool {
	return len(s) != 0 && (s[0] <= 0x20 || s[len(s)-1] <= 0x20)
}
