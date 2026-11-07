package connections

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DorisOptions contains only public settings. The first native adapter supports
// the internal catalog; external connectors require separate verification.
type DorisOptions struct {
	Version    int    `json:"version"`
	Catalog    string `json:"catalog"`
	CAFile     string `json:"ca_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func normalizeDorisProfile(p Profile) (Profile, error) {
	a := &p.DorisOptions
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Catalog == "" {
		a.Catalog = "internal"
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	invalid := func() (Profile, error) { return p, fmt.Errorf("%w: invalid or unsupported Doris settings", ErrInvalid) }
	if a.Version != 1 || a.Catalog != "internal" || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Host == "" || p.Port == 0 || p.Username == "" {
		return invalid()
	}
	for _, v := range []string{p.Host, p.Username, p.Database, a.CAFile, a.ServerName} {
		if !utf8.ValidString(v) || len(v) > 4096 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return invalid()
		}
	}
	if strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
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
