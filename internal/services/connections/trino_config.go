package connections

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TrinoConfig is the public native HTTP connection configuration. Credentials
// are supplied separately. Profile persistence and workbench wiring follow the
// transport foundation; this type does not advertise a registered product.
type TrinoConfig struct {
	Endpoint, Username, Catalog, Schema, CAFile, ServerName string
}

func NormalizeTrinoConfig(c TrinoConfig) (TrinoConfig, error) {
	invalid := func() (TrinoConfig, error) { return c, fmt.Errorf("%w: invalid Trino HTTP configuration", ErrInvalid) }
	for _, s := range []string{c.Endpoint, c.Username, c.Catalog, c.Schema, c.CAFile, c.ServerName} {
		if !utf8.ValidString(s) || len(s) > 4096 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return invalid()
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && u.Scheme != "http" {
		return invalid()
	}
	if c.Username == "" || strings.ContainsAny(c.Username, ":,") || c.Schema != "" && c.Catalog == "" {
		return invalid()
	}
	if u.Scheme == "http" && (c.CAFile != "" || c.ServerName != "") || c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		return invalid()
	}
	u.Path = ""
	c.Endpoint = u.String()
	return c, nil
}
