package connections

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RQLiteOptions describes one original HTTP endpoint. Passwords and private
// certificate contents remain outside profiles. Writes are never retried.
type RQLiteOptions struct {
	Version                         int    `json:"version"`
	Auth                            string `json:"auth"`
	BasePath                        string `json:"base_path"`
	Consistency                     string `json:"consistency"`
	FreshnessMilliseconds           int    `json:"freshness_milliseconds"`
	FreshnessStrict                 bool   `json:"freshness_strict,omitempty"`
	ConnectTimeoutSeconds           int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds           int    `json:"request_timeout_seconds"`
	ForwardTimeoutSeconds           int    `json:"forward_timeout_seconds"`
	DatabaseTimeoutMilliseconds     int    `json:"database_timeout_milliseconds"`
	LinearizableTimeoutMilliseconds int    `json:"linearizable_timeout_milliseconds"`
	MaxRequests                     int    `json:"max_requests"`
	SQLMaxConnections               int    `json:"sql_max_connections"`
	BatchTransaction                bool   `json:"batch_transaction,omitempty"`
	ReadOnly                        bool   `json:"read_only,omitempty"`
	CAFile                          string `json:"ca_file,omitempty"`
	ServerName                      string `json:"server_name,omitempty"`
	ClientCertFile                  string `json:"client_cert_file,omitempty"`
	ClientKeyFile                   string `json:"client_key_file,omitempty"`
}

func DefaultRQLiteOptions() RQLiteOptions {
	return RQLiteOptions{Version: 1, Auth: "none", Consistency: "weak", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 60, ForwardTimeoutSeconds: 30, DatabaseTimeoutMilliseconds: 30000, LinearizableTimeoutMilliseconds: 10000, MaxRequests: 8, SQLMaxConnections: 6}
}

func NormalizeRQLiteOptions(o RQLiteOptions) (RQLiteOptions, error) {
	if o == (RQLiteOptions{}) {
		o = DefaultRQLiteOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" || o.Consistency != "none" && o.Consistency != "weak" && o.Consistency != "strong" && o.Consistency != "linearizable" || o.ConnectTimeoutSeconds < 1 || o.ConnectTimeoutSeconds > 300 || o.RequestTimeoutSeconds < 1 || o.RequestTimeoutSeconds > 86400 || o.ForwardTimeoutSeconds < 1 || o.ForwardTimeoutSeconds >= o.RequestTimeoutSeconds || o.DatabaseTimeoutMilliseconds < 1 || o.DatabaseTimeoutMilliseconds >= o.RequestTimeoutSeconds*1000 || o.LinearizableTimeoutMilliseconds < 1 || o.LinearizableTimeoutMilliseconds >= o.RequestTimeoutSeconds*1000 || o.MaxRequests < 1 || o.MaxRequests > 32 || o.SQLMaxConnections < 1 || o.SQLMaxConnections > o.MaxRequests {
		return o, ErrInvalid
	}
	if o.FreshnessMilliseconds < 0 || o.FreshnessMilliseconds > 86400000 || (o.FreshnessMilliseconds != 0 || o.FreshnessStrict) && o.Consistency != "none" || o.FreshnessStrict && o.FreshnessMilliseconds == 0 {
		return o, ErrInvalid
	}
	if !validEngineText(o.BasePath, 4096) || o.BasePath != "" && (!strings.HasPrefix(o.BasePath, "/") || strings.HasSuffix(o.BasePath, "/") || strings.ContainsAny(o.BasePath, "?#%\\") || strings.Contains(o.BasePath, "//")) {
		return o, ErrInvalid
	}
	for _, segment := range strings.Split(o.BasePath, "/") {
		if segment == "." || segment == ".." {
			return o, ErrInvalid
		}
	}
	for _, path := range []string{o.CAFile, o.ClientCertFile, o.ClientKeyFile} {
		if !validEngineText(path, 4096) || path != "" && !filepath.IsAbs(path) {
			return o, ErrInvalid
		}
	}
	if !validEngineText(o.ServerName, 255) || strings.ContainsAny(o.ServerName, "[]@/?!#\\: ") || (o.ClientCertFile == "") != (o.ClientKeyFile == "") {
		return o, ErrInvalid
	}
	return o, nil
}

type RQLiteConfig struct {
	Endpoint, Username string
	Options            RQLiteOptions
}

func RQLiteProfileConfig(p Profile) (RQLiteConfig, error) {
	o, err := NormalizeRQLiteOptions(p.RQLiteOptions)
	if err != nil || p.Kind != RQLite || p.Database != "main" || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Host == "" || p.Port == 0 || !validEngineText(p.Host, 255) || strings.ContainsAny(p.Host, "[]@/?!#\\, \"'<>{}|^`%") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || !validEngineText(p.Username, 4096) || strings.Contains(p.Username, ":") || o.Auth == "none" && p.Username != "" || o.Auth == "basic" && p.Username == "" || p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-full" {
		return RQLiteConfig{}, ErrInvalid
	}
	if p.TLSMode == "disable" && (o.CAFile != "" || o.ServerName != "" || o.ClientCertFile != "") || p.TLSMode == "require" && (o.CAFile != "" || o.ServerName != "") {
		return RQLiteConfig{}, ErrInvalid
	}
	scheme := "http"
	if p.TLSMode != "disable" {
		scheme = "https"
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: o.BasePath}
	parsed, err := url.Parse(u.String())
	if err != nil || parsed.Hostname() != p.Host || parsed.User != nil {
		return RQLiteConfig{}, ErrInvalid
	}
	return RQLiteConfig{Endpoint: u.String(), Username: p.Username, Options: o}, nil
}

func ValidateRQLiteCredential(c RQLiteConfig, password string) error {
	if !utf8.ValidString(password) || len(password) > 4096 || strings.ContainsRune(password, 0) || c.Options.Auth != "basic" && password != "" {
		return ErrInvalid
	}
	return nil // Empty Basic passwords are valid credentials, checked by the server.
}

func normalizeRQLiteProfile(p Profile) (Profile, error) {
	o, err := NormalizeRQLiteOptions(p.RQLiteOptions)
	if err != nil {
		return p, err
	}
	p.RQLiteOptions = o
	if p.Database == "" {
		p.Database = "main"
	}
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	_, err = RQLiteProfileConfig(p)
	return p, err
}

// ParseRQLiteURI captures userinfo separately and rejects duplicate or unknown
// settings, including native queue/retries/redirect options with unsafe replay.
func ParseRQLiteURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(RQLite)
	switch u.Scheme {
	case "rqlite":
	case "rqlites":
		p.TLSMode = "verify-full"
	default:
		return fail()
	}
	p.Host = u.Hostname()
	if port := u.Port(); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	p.RQLiteOptions.BasePath = strings.TrimSuffix(u.Path, "/")
	secret := ""
	if u.User != nil {
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
		p.RQLiteOptions.Auth = "basic"
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range query {
		if len(values) != 1 {
			return fail()
		}
		v := values[0]
		o := &p.RQLiteOptions
		switch key {
		case "auth":
			o.Auth = v
		case "consistency":
			o.Consistency = v
		case "tls_mode":
			p.TLSMode = v
		case "ca_file":
			o.CAFile = v
		case "server_name":
			o.ServerName = v
		case "client_cert_file":
			o.ClientCertFile = v
		case "client_key_file":
			o.ClientKeyFile = v
		case "read_only", "batch_transaction", "freshness_strict":
			if v != "true" && v != "false" {
				return fail()
			}
			if key == "read_only" {
				o.ReadOnly = v == "true"
			} else if key == "batch_transaction" {
				o.BatchTransaction = v == "true"
			} else {
				o.FreshnessStrict = v == "true"
			}
		case "freshness_milliseconds", "connect_timeout_seconds", "request_timeout_seconds", "forward_timeout_seconds", "database_timeout_milliseconds", "linearizable_timeout_milliseconds", "max_requests", "sql_max_connections":
			n, e := strconv.ParseUint(v, 10, 31)
			if e != nil || strconv.FormatUint(n, 10) != v {
				return fail()
			}
			switch key {
			case "freshness_milliseconds":
				o.FreshnessMilliseconds = int(n)
			case "connect_timeout_seconds":
				o.ConnectTimeoutSeconds = int(n)
			case "request_timeout_seconds":
				o.RequestTimeoutSeconds = int(n)
			case "forward_timeout_seconds":
				o.ForwardTimeoutSeconds = int(n)
			case "database_timeout_milliseconds":
				o.DatabaseTimeoutMilliseconds = int(n)
			case "linearizable_timeout_milliseconds":
				o.LinearizableTimeoutMilliseconds = int(n)
			case "max_requests":
				o.MaxRequests = int(n)
			case "sql_max_connections":
				o.SQLMaxConnections = int(n)
			}
		default:
			return fail()
		}
	}
	if u.Scheme == "rqlites" && p.TLSMode == "disable" || u.Scheme == "rqlite" && p.TLSMode != "disable" || u.User != nil && p.RQLiteOptions.Auth != "basic" {
		return fail()
	}
	p.Name = "RQLite · " + p.Host
	if utf8.RuneCountInString(p.Name) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := RQLiteProfileConfig(p)
	if err != nil || ValidateRQLiteCredential(cfg, secret) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
