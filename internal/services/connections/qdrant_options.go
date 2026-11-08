package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// QdrantOptions holds versioned public REST settings, separate from the API
// key. ContextPath is an HTTP service prefix, not a database or collection.
type QdrantOptions struct {
	Version               int    `json:"version"`
	Auth                  string `json:"auth"`
	ContextPath           string `json:"context_path"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

func DefaultQdrantOptions() QdrantOptions {
	return QdrantOptions{Version: 1, Auth: "none", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 40}
}

func NormalizeQdrantOptions(o QdrantOptions) (QdrantOptions, error) {
	if o == (QdrantOptions{}) {
		o = DefaultQdrantOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "api_key" || o.ContextPath != "" && !strings.HasPrefix(o.ContextPath, "/") {
		return o, ErrInvalid
	}
	c, err := NormalizeQdrantConfig(QdrantConfig{Endpoint: "https://localhost:6333" + o.ContextPath, Auth: o.Auth, CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
	if err != nil {
		return o, err
	}
	o.ContextPath = strings.TrimPrefix(c.Endpoint, "https://localhost:6333")
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = c.ConnectTimeoutSeconds, c.RequestTimeoutSeconds
	return o, nil
}

func normalizeQdrantProfile(p Profile) (Profile, error) {
	var err error
	p.QdrantOptions, err = NormalizeQdrantOptions(p.QdrantOptions)
	if err != nil {
		return p, err
	}
	_, err = QdrantProfileConfig(p)
	return p, err
}

func QdrantProfileConfig(p Profile) (QdrantConfig, error) {
	if p.Kind != Qdrant || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Username != "" || p.Host == "" || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return QdrantConfig{}, ErrInvalid
	}
	o, err := NormalizeQdrantOptions(p.QdrantOptions)
	if err != nil {
		return QdrantConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeQdrantConfig(QdrantConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))) + o.ContextPath, Auth: o.Auth, CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
}

// ParseQdrantURI performs no network, file or vault operations. API keys
// remain temporary credentials outside the public profile.
func ParseQdrantURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "qdrant") || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p, _ := Defaults(Qdrant)
	p.Host, p.QdrantOptions.ContextPath = u.Hostname(), u.Path
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
		p.QdrantOptions.Auth = "api_key"
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
			p.QdrantOptions.Auth = value
		case "api_key":
			if u.User != nil {
				return fail()
			}
			secret = value
		case "sslmode":
			p.TLSMode = value
		case "ca_file":
			p.QdrantOptions.CAFile = value
		case "server_name":
			p.QdrantOptions.ServerName = value
		case "client_cert_file":
			p.QdrantOptions.ClientCertFile = value
		case "client_key_file":
			p.QdrantOptions.ClientKeyFile = value
		case "connect_timeout", "request_timeout":
			n, e := strconv.Atoi(value)
			if e != nil || n <= 0 || strconv.Itoa(n) != value {
				return fail()
			}
			if key == "connect_timeout" {
				p.QdrantOptions.ConnectTimeoutSeconds = n
			} else {
				p.QdrantOptions.RequestTimeoutSeconds = n
			}
		default:
			return fail()
		}
	}
	if _, present := options["api_key"]; present {
		if auth, explicit := options["auth"]; explicit && auth[0] != "api_key" {
			return fail()
		}
		p.QdrantOptions.Auth = "api_key"
	}
	if u.User != nil && p.QdrantOptions.Auth != "api_key" {
		return fail()
	}
	p.Name = "Qdrant · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := QdrantProfileConfig(p)
	if err != nil || ValidateQdrantCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
