package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CouchDBOptions stores versioned public settings. ContextPath is an HTTP
// service prefix; database names belong to native requests and metadata.
type CouchDBOptions struct {
	Version     int    `json:"version"`
	Auth        string `json:"auth"`
	ContextPath string `json:"context_path"`
	CAFile      string `json:"ca_file,omitempty"`
	ServerName  string `json:"server_name,omitempty"`
}

func DefaultCouchDBOptions() CouchDBOptions { return CouchDBOptions{Version: 1, Auth: "none"} }

func NormalizeCouchDBOptions(o CouchDBOptions) (CouchDBOptions, error) {
	if o == (CouchDBOptions{}) {
		o = DefaultCouchDBOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" || o.ContextPath != "" && !strings.HasPrefix(o.ContextPath, "/") {
		return o, ErrInvalid
	}
	c, err := NormalizeCouchDBConfig(CouchDBConfig{Endpoint: "https://localhost:5984" + o.ContextPath, CAFile: o.CAFile, ServerName: o.ServerName})
	if err != nil {
		return o, err
	}
	o.ContextPath = strings.TrimPrefix(c.Endpoint, "https://localhost:5984")
	return o, nil
}

func normalizeCouchDBProfile(p Profile) (Profile, error) {
	var err error
	p.CouchDBOptions, err = NormalizeCouchDBOptions(p.CouchDBOptions)
	if err != nil {
		return p, err
	}
	_, err = CouchDBProfileConfig(p)
	return p, err
}

func CouchDBProfileConfig(p Profile) (CouchDBConfig, error) {
	if p.Kind != CouchDB || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Port == 0 || p.Host == "" || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "verify-full" && p.TLSMode != "disable" {
		return CouchDBConfig{}, ErrInvalid
	}
	o, err := NormalizeCouchDBOptions(p.CouchDBOptions)
	if err != nil {
		return CouchDBConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeCouchDBConfig(CouchDBConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))) + o.ContextPath, Auth: o.Auth, Username: p.Username, CAFile: o.CAFile, ServerName: o.ServerName})
}

// ParseCouchDBURI is pure and keeps credentials separate from Profile.
func ParseCouchDBURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "couchdb") || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(CouchDB)
	p.Host, p.CouchDBOptions.ContextPath = u.Hostname(), u.Path
	if rawPort := u.Port(); rawPort != "" {
		n, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	if u.User != nil {
		p.CouchDBOptions.Auth = "basic"
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		switch key {
		case "auth":
			p.CouchDBOptions.Auth = values[0]
		case "sslmode":
			p.TLSMode = values[0]
		case "ca_file":
			p.CouchDBOptions.CAFile = values[0]
		case "server_name":
			p.CouchDBOptions.ServerName = values[0]
		default:
			return fail()
		}
	}
	secret := ""
	if u.User != nil {
		if p.CouchDBOptions.Auth != "basic" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "Apache CouchDB · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := CouchDBProfileConfig(p)
	if err != nil || ValidateCouchDBCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
