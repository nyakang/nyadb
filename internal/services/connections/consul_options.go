package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ConsulOptions contains original public settings only. Token and TLS key
// contents never enter profiles; mTLS is independent of token authorization.
type ConsulOptions struct {
	Version               int    `json:"version"`
	Auth                  string `json:"auth"`
	Datacenter            string `json:"datacenter"`
	Prefix                string `json:"prefix"`
	Consistency           string `json:"consistency"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

func DefaultConsulOptions() ConsulOptions {
	return ConsulOptions{Version: 1, Auth: "none", Consistency: "default", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 30}
}

func NormalizeConsulOptions(o ConsulOptions) (ConsulOptions, error) {
	if o == (ConsulOptions{}) {
		o = DefaultConsulOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "token" || o.Consistency != "default" && o.Consistency != "consistent" && o.Consistency != "stale" || o.ConnectTimeoutSeconds < 1 || o.ConnectTimeoutSeconds > 300 || o.RequestTimeoutSeconds < 1 || o.RequestTimeoutSeconds > 600 {
		return o, ErrInvalid
	}
	if !validEngineText(o.Datacenter, 255) || strings.ContainsAny(o.Datacenter, "*/?\\ ") || !utf8.ValidString(o.Prefix) || len(o.Prefix) > 4096 || strings.ContainsRune(o.Prefix, 0) {
		return o, ErrInvalid
	}
	for _, text := range []string{o.CAFile, o.ServerName, o.ClientCertFile, o.ClientKeyFile} {
		if !validEngineText(text, 4096) {
			return o, ErrInvalid
		}
	}
	if len(o.ServerName) > 255 || strings.ContainsAny(o.ServerName, " /\\") || (o.ClientCertFile == "") != (o.ClientKeyFile == "") {
		return o, ErrInvalid
	}
	return o, nil
}

type ConsulConfig struct {
	Endpoint string
	Options  ConsulOptions
}

func ConsulProfileConfig(p Profile) (ConsulConfig, error) {
	if p.Kind != Consul || p.Database != "" || p.Username != "" || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Port == 0 || p.Host == "" || !validEngineText(p.Host, 255) || strings.ContainsAny(p.Host, "[]@/?!#\\, ") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return ConsulConfig{}, ErrInvalid
	}
	o, err := NormalizeConsulOptions(p.ConsulOptions)
	if err != nil {
		return ConsulConfig{}, err
	}
	if p.TLSMode == "disable" && (o.CAFile != "" || o.ServerName != "" || o.ClientCertFile != "") {
		return ConsulConfig{}, ErrInvalid
	}
	scheme := "http"
	if p.TLSMode == "verify-full" {
		scheme = "https"
	}
	return ConsulConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Options: o}, nil
}

func ValidateConsulCredential(c ConsulConfig, token string, required bool) error {
	if !validEngineText(token, 4096) || strings.Contains(token, " ") || c.Options.Auth != "token" && token != "" || required && c.Options.Auth == "token" && token == "" {
		return ErrInvalid
	}
	return nil
}

func normalizeConsulProfile(p Profile) (Profile, error) {
	o, err := NormalizeConsulOptions(p.ConsulOptions)
	if err != nil {
		return p, err
	}
	p.ConsulOptions = o
	_, err = ConsulProfileConfig(p)
	return p, err
}

// ParseConsulURI moves optional :token userinfo into the temporary credential.
// DC and prefix stay byte-exact; namespace/partition, credential query values,
// duplicate options and unsupported TLS modes are rejected explicitly.
func ParseConsulURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || u.Path != "" && u.Path != "/" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(Consul)
	switch strings.ToLower(u.Scheme) {
	case "consul":
	case "consuls":
		p.TLSMode = "verify-full"
		p.Port = 8501
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
	secret := ""
	if u.User != nil {
		var present bool
		secret, present = u.User.Password()
		if u.User.Username() != "" || !present {
			return fail()
		}
		p.ConsulOptions.Auth = "token"
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range query {
		if len(values) != 1 {
			return fail()
		}
		value := values[0]
		o := &p.ConsulOptions
		switch key {
		case "auth":
			o.Auth = value
		case "dc":
			o.Datacenter = value
		case "prefix":
			o.Prefix = value
		case "consistency":
			o.Consistency = value
		case "ca_file":
			o.CAFile = value
		case "server_name":
			o.ServerName = value
		case "client_cert_file":
			o.ClientCertFile = value
		case "client_key_file":
			o.ClientKeyFile = value
		case "connect_timeout_seconds", "request_timeout_seconds":
			n, e := strconv.ParseUint(value, 10, 16)
			if e != nil || n == 0 || strconv.FormatUint(n, 10) != value {
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
	if u.User != nil && p.ConsulOptions.Auth != "token" {
		return fail()
	}
	p.Name = "Consul · " + p.Host
	if utf8.RuneCountInString(p.Name) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := ConsulProfileConfig(p)
	if err != nil || ValidateConsulCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
