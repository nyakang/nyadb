package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CassandraOptions is a versioned public native configuration. Profile.Database
// is the optional initial Keyspace; credentials are never part of this value.
type CassandraOptions struct {
	Version    int    `json:"version"`
	Auth       string `json:"auth"`
	CAFile     string `json:"ca_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func DefaultCassandraOptions() CassandraOptions { return CassandraOptions{Version: 1, Auth: "none"} }

func CassandraProfileConfig(p Profile) (CassandraConfig, error) {
	if p.Kind != Cassandra || p.Host == "" || p.Port == 0 || p.MySQLAdvanced != (MySQLAdvanced{}) || strings.ContainsAny(p.Host, "[]@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
		return CassandraConfig{}, ErrInvalid
	}
	o := p.CassandraOptions
	if o == (CassandraOptions{}) {
		o = DefaultCassandraOptions()
	}
	if o.Version != 1 {
		return CassandraConfig{}, ErrInvalid
	}
	return NormalizeCassandraConfig(CassandraConfig{Endpoint: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Auth: o.Auth, Username: p.Username, TLSMode: p.TLSMode, CAFile: o.CAFile, ServerName: o.ServerName, Keyspace: p.Database})
}

func normalizeCassandraProfile(p Profile) (Profile, error) {
	c, err := CassandraProfileConfig(p)
	if err != nil {
		return p, err
	}
	p.TLSMode = c.TLSMode
	p.CassandraOptions = CassandraOptions{Version: 1, Auth: c.Auth, CAFile: c.CAFile, ServerName: c.ServerName}
	return p, nil
}

// ParseCassandraURI is pure configuration parsing for private editor workflows.
// Public ParseURI remains gated until the provider and workbench are registered.
func ParseCassandraURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.ContainsRune(raw, '#') {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "cassandra") || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(Cassandra)
	p.Host = u.Hostname()
	if port := u.Port(); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	if u.Path != "" {
		if !strings.HasPrefix(u.Path, "/") {
			return fail()
		}
		p.Database = strings.TrimPrefix(u.Path, "/")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	if u.User != nil {
		p.CassandraOptions.Auth = "password"
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		switch key {
		case "auth":
			p.CassandraOptions.Auth = values[0]
		case "sslmode":
			p.TLSMode = values[0]
		case "ca_file":
			p.CassandraOptions.CAFile = values[0]
		case "server_name":
			p.CassandraOptions.ServerName = values[0]
		default:
			return fail()
		}
	}
	secret := ""
	if u.User != nil {
		if p.CassandraOptions.Auth != "password" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "Apache Cassandra · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	c, err := CassandraProfileConfig(p)
	if err != nil || ValidateCassandraCredential(c, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
