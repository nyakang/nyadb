package connections

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Database is the optional default catalog; schema is a separate public option.
// These defaults never constrain the qualified catalog/schema navigator.
type TrinoOptions struct {
	Version       int    `json:"version"`
	DefaultSchema string `json:"default_schema,omitempty"`
	CAFile        string `json:"ca_file,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
}

func normalizeTrinoProfile(p Profile) (Profile, error) {
	for _, name := range []string{p.Database, p.TrinoOptions.DefaultSchema} {
		if len(name) > 256 {
			return p, fmt.Errorf("%w: Trino catalog or schema exceeds 256 bytes", ErrInvalid)
		}
	}
	if p.TrinoOptions.Version == 0 {
		p.TrinoOptions.Version = 1
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	if p.TrinoOptions.Version != 1 || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return p, fmt.Errorf("%w: invalid Trino profile", ErrInvalid)
	}
	_, err := TrinoProfileConfig(p)
	return p, err
}

func TrinoProfileConfig(p Profile) (TrinoConfig, error) {
	if p.Kind != Trino {
		return TrinoConfig{}, ErrInvalid
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeTrinoConfig(TrinoConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Username: p.Username, Catalog: p.Database, Schema: p.TrinoOptions.DefaultSchema, CAFile: p.TrinoOptions.CAFile, ServerName: p.TrinoOptions.ServerName})
}

func parseTrinoURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Trino)
	p.Host = u.Hostname()
	if p.Host == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	if raw := u.Port(); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if path != "" {
		parts := strings.Split(path, "/")
		if len(parts) > 2 {
			return fail()
		}
		var err error
		p.Database, err = url.PathUnescape(parts[0])
		if err != nil || p.Database == "" {
			return fail()
		}
		if len(parts) == 2 {
			p.TrinoOptions.DefaultSchema, err = url.PathUnescape(parts[1])
			if err != nil || p.TrinoOptions.DefaultSchema == "" {
				return fail()
			}
		}
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for k, v := range options {
		if len(v) != 1 || v[0] == "" {
			return fail()
		}
		switch k {
		case "sslmode":
			p.TLSMode = v[0]
		case "schema":
			if p.TrinoOptions.DefaultSchema != "" {
				return fail()
			}
			p.TrinoOptions.DefaultSchema = v[0]
		case "ca_file":
			p.TrinoOptions.CAFile = v[0]
		case "server_name":
			p.TrinoOptions.ServerName = v[0]
		default:
			return fail()
		}
	}
	if p.TLSMode == "disable" && password != "" {
		return fail()
	}
	for _, v := range []string{p.Database, p.TrinoOptions.DefaultSchema} {
		if len(v) > 256 || !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fail()
		}
	}
	p.Name = "Trino · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: password}, nil
}
