package connections

import (
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EtcdOptions is a comparable versioned public snapshot. Prefix is never
// trimmed or treated as a database; binary keys use explicit canonical Base64.
type EtcdOptions struct {
	Version               int    `json:"version"`
	Auth                  string `json:"auth"`
	PrefixEncoding        string `json:"prefix_encoding"`
	Prefix                string `json:"prefix"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

func DefaultEtcdOptions() EtcdOptions {
	return EtcdOptions{Version: 1, Auth: "none", PrefixEncoding: "text", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 30}
}

func EtcdPrefixBytes(o EtcdOptions) ([]byte, error) {
	var b []byte
	switch o.PrefixEncoding {
	case "text":
		if !utf8.ValidString(o.Prefix) || strings.IndexFunc(o.Prefix, unicode.IsControl) >= 0 {
			return nil, ErrInvalid
		}
		b = []byte(o.Prefix)
	case "base64":
		if len(o.Prefix) > 5464 {
			return nil, ErrInvalid
		}
		var err error
		b, err = base64.StdEncoding.Strict().DecodeString(o.Prefix)
		if err != nil || base64.StdEncoding.EncodeToString(b) != o.Prefix {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	if len(b) > 4096 {
		return nil, ErrInvalid
	}
	return b, nil
}

func NormalizeEtcdOptions(o EtcdOptions) (EtcdOptions, error) {
	if o == (EtcdOptions{}) {
		o = DefaultEtcdOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" && o.Auth != "certificate" || o.ConnectTimeoutSeconds < 1 || o.ConnectTimeoutSeconds > 300 || o.RequestTimeoutSeconds < 1 || o.RequestTimeoutSeconds > 600 {
		return o, ErrInvalid
	}
	if _, err := EtcdPrefixBytes(o); err != nil {
		return o, err
	}
	for _, s := range []string{o.CAFile, o.ClientCertFile, o.ClientKeyFile, o.ServerName} {
		if !utf8.ValidString(s) || len(s) > 4096 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return o, ErrInvalid
		}
	}
	if len(o.ServerName) > 255 || strings.ContainsAny(o.ServerName, " /\\") || (o.ClientCertFile == "") != (o.ClientKeyFile == "") || o.Auth == "certificate" && o.ClientCertFile == "" || o.Auth == "none" && o.ClientCertFile != "" {
		return o, ErrInvalid
	}
	return o, nil
}

// EtcdConfig is pure public configuration. File contents and temporary secrets
// are consumed only by the native service; PrefixBytes always returns a copy.
type EtcdConfig struct {
	Endpoint, Username, TLSMode string
	Options                     EtcdOptions
}

func EtcdProfileConfig(p Profile) (EtcdConfig, error) {
	if p.Kind != Etcd || p.Database != "" || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Port == 0 || p.Host == "" || len(p.Host) > 255 || strings.IndexFunc(p.Host, unicode.IsControl) >= 0 || !utf8.ValidString(p.Host) || strings.ContainsAny(p.Host, "[]@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return EtcdConfig{}, ErrInvalid
	}
	o, err := NormalizeEtcdOptions(p.EtcdOptions)
	if err != nil {
		return EtcdConfig{}, err
	}
	if !utf8.ValidString(p.Username) || len(p.Username) > 4096 || strings.ContainsRune(p.Username, 0) || o.Auth == "basic" && p.Username == "" || o.Auth != "basic" && p.Username != "" {
		return EtcdConfig{}, ErrInvalid
	}
	if p.TLSMode == "disable" && (o.CAFile != "" || o.ServerName != "" || o.ClientCertFile != "" || o.Auth == "certificate") {
		return EtcdConfig{}, ErrInvalid
	}
	scheme := "http"
	if p.TLSMode == "verify-full" {
		scheme = "https"
	}
	return EtcdConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Username: p.Username, TLSMode: p.TLSMode, Options: o}, nil
}

func ValidateEtcdCredential(c EtcdConfig, secret string, required bool) error {
	if !utf8.ValidString(secret) || len(secret) > 1<<20 || c.Options.Auth != "basic" && secret != "" || required && c.Options.Auth == "basic" && secret == "" {
		return ErrInvalid
	}
	return nil
}

func normalizeEtcdProfile(p Profile) (Profile, error) {
	o, err := NormalizeEtcdOptions(p.EtcdOptions)
	if err != nil {
		return p, err
	}
	p.EtcdOptions = o
	_, err = EtcdProfileConfig(p)
	return p, err
}

// ParseEtcdURI is product-specific and pure. The private profile phase does not
// route general URI imports or register a runtime provider.
func ParseEtcdURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 32<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.ForceQuery || u.Path != "" && u.Path != "/" || u.RawPath != "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(Etcd)
	switch strings.ToLower(u.Scheme) {
	case "etcd":
	case "etcds":
		p.TLSMode = "verify-full"
	default:
		return fail()
	}
	p.Host = u.Hostname()
	if u.Port() != "" {
		port, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil || port == 0 {
			return fail()
		}
		p.Port = uint16(port)
	}
	if u.User != nil {
		p.EtcdOptions.Auth = "basic"
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, v := range values {
		if len(v) != 1 {
			return fail()
		}
		o := &p.EtcdOptions
		switch key {
		case "auth":
			o.Auth = v[0]
		case "prefix":
			o.Prefix = v[0]
		case "prefix_encoding":
			o.PrefixEncoding = v[0]
		case "ca_file":
			o.CAFile = v[0]
		case "server_name":
			o.ServerName = v[0]
		case "client_cert_file":
			o.ClientCertFile = v[0]
		case "client_key_file":
			o.ClientKeyFile = v[0]
		case "connect_timeout_seconds", "request_timeout_seconds":
			n, e := strconv.ParseUint(v[0], 10, 16)
			if e != nil || n == 0 || strconv.FormatUint(n, 10) != v[0] {
				return fail()
			}
			if key == "connect_timeout_seconds" {
				o.ConnectTimeoutSeconds = int(n)
			} else {
				o.RequestTimeoutSeconds = int(n)
			}
		default:
			return fail()
		}
	}
	secret := ""
	if u.User != nil {
		if p.EtcdOptions.Auth != "basic" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "etcd · " + p.Host
	if utf8.RuneCountInString(p.Name) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := EtcdProfileConfig(p)
	if err != nil || ValidateEtcdCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
