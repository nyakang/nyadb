package connections

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SelectDBOptions preserves the original catalog separately from the database.
// It contains no credentials or driver state.
type SelectDBOptions struct {
	Version    int    `json:"version"`
	Catalog    string `json:"catalog"`
	CAFile     string `json:"ca_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func normalizeSelectDBProfile(p Profile) (Profile, error) {
	a := &p.SelectDBOptions
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Catalog == "" {
		a.Catalog = "internal"
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	invalid := func() (Profile, error) { return p, fmt.Errorf("%w: invalid SelectDB settings", ErrInvalid) }
	if a.Version != 1 || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Host == "" || p.Port == 0 || p.Username == "" {
		return invalid()
	}
	for _, v := range []string{p.Host, p.Username, p.Database, a.Catalog, a.CAFile, a.ServerName} {
		if !utf8.ValidString(v) || len(v) > 4096 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return invalid()
		}
	}
	if len(a.Catalog) > 1024 || len(p.Database) > 1024 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
		return invalid()
	}
	if p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return invalid()
	}
	if p.TLSMode == "disable" && (a.CAFile != "" || a.ServerName != "") {
		return invalid()
	}
	if a.CAFile != "" && !filepath.IsAbs(a.CAFile) {
		return invalid()
	}
	if strings.ContainsAny(a.ServerName, "@/?!#\\: \t\r\n") && net.ParseIP(a.ServerName) == nil {
		return invalid()
	}
	return p, nil
}
