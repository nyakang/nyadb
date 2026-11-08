package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MeilisearchOptions contains versioned public settings only. IndexUID selects
// an explicit scoped handshake; it is separate from the HTTP context prefix.
type MeilisearchOptions struct {
	Version               int    `json:"version"`
	Auth                  string `json:"auth"`
	ContextPath           string `json:"context_path"`
	IndexUID              string `json:"index_uid"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

func DefaultMeilisearchOptions() MeilisearchOptions {
	return MeilisearchOptions{Version: 1, Auth: "none", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 40}
}

func NormalizeMeilisearchOptions(o MeilisearchOptions) (MeilisearchOptions, error) {
	if o == (MeilisearchOptions{}) {
		o = DefaultMeilisearchOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "bearer" || o.ContextPath != "" && !strings.HasPrefix(o.ContextPath, "/") {
		return o, ErrInvalid
	}
	c, err := NormalizeMeilisearchConfig(MeilisearchConfig{Endpoint: "https://localhost:7700" + o.ContextPath, Auth: o.Auth, IndexUID: o.IndexUID, CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
	if err != nil {
		return o, err
	}
	o.ContextPath = strings.TrimPrefix(c.Endpoint, "https://localhost:7700")
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = c.ConnectTimeoutSeconds, c.RequestTimeoutSeconds
	return o, nil
}

func normalizeMeilisearchProfile(p Profile) (Profile, error) {
	var err error
	p.MeilisearchOptions, err = NormalizeMeilisearchOptions(p.MeilisearchOptions)
	if err != nil {
		return p, err
	}
	_, err = MeilisearchProfileConfig(p)
	return p, err
}

func MeilisearchProfileConfig(p Profile) (MeilisearchConfig, error) {
	if p.Kind != Meilisearch || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Username != "" || p.Host == "" || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return MeilisearchConfig{}, ErrInvalid
	}
	o, err := NormalizeMeilisearchOptions(p.MeilisearchOptions)
	if err != nil {
		return MeilisearchConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeMeilisearchConfig(MeilisearchConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))) + o.ContextPath, Auth: o.Auth, IndexUID: o.IndexUID, CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
}

// ParseMeilisearchURI performs no network, file or vault operations. API keys
// remain temporary credentials outside the public profile.
func ParseMeilisearchURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "meilisearch") || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(Meilisearch)
	p.Host, p.MeilisearchOptions.ContextPath = u.Hostname(), u.Path
	if port := u.Port(); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	secret := ""
	if u.User != nil {
		if u.User.Username() != "" {
			return fail()
		}
		var present bool
		secret, present = u.User.Password()
		if !present {
			return fail()
		}
		p.MeilisearchOptions.Auth = "bearer"
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		value := values[0]
		switch key {
		case "auth":
			p.MeilisearchOptions.Auth = value
		case "api_key":
			if u.User != nil {
				return fail()
			}
			secret = value
		case "sslmode":
			p.TLSMode = value
		case "index_uid":
			p.MeilisearchOptions.IndexUID = value
		case "ca_file":
			p.MeilisearchOptions.CAFile = value
		case "server_name":
			p.MeilisearchOptions.ServerName = value
		case "client_cert_file":
			p.MeilisearchOptions.ClientCertFile = value
		case "client_key_file":
			p.MeilisearchOptions.ClientKeyFile = value
		case "connect_timeout", "request_timeout":
			n, e := strconv.Atoi(value)
			if e != nil || n <= 0 || strconv.Itoa(n) != value {
				return fail()
			}
			if key == "connect_timeout" {
				p.MeilisearchOptions.ConnectTimeoutSeconds = n
			} else {
				p.MeilisearchOptions.RequestTimeoutSeconds = n
			}
		default:
			return fail()
		}
	}
	if _, present := options["api_key"]; present {
		if auth, explicit := options["auth"]; explicit && auth[0] != "bearer" {
			return fail()
		}
		p.MeilisearchOptions.Auth = "bearer"
	}
	if u.User != nil && p.MeilisearchOptions.Auth != "bearer" {
		return fail()
	}
	p.Name = "Meilisearch · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := MeilisearchProfileConfig(p)
	if err != nil || ValidateMeilisearchCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
