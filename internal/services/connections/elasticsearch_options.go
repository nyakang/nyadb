package connections

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// ElasticsearchOptions is a public versioned envelope. Passwords and encoded
// API keys use the same separate primary credential slot, never these options.
type ElasticsearchOptions struct {
	Version    int    `json:"version"`
	Auth       string `json:"auth"`
	CAFile     string `json:"ca_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func DefaultElasticsearchOptions() ElasticsearchOptions {
	return ElasticsearchOptions{Version: 1, Auth: "basic"}
}

func NormalizeElasticsearchOptions(o ElasticsearchOptions) (ElasticsearchOptions, error) {
	if o == (ElasticsearchOptions{}) {
		o = DefaultElasticsearchOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" && o.Auth != "api_key" {
		return o, ErrInvalid
	}
	if !validEngineText(o.CAFile, 4096) || !validEngineText(o.ServerName, 4096) || o.CAFile != "" && !filepath.IsAbs(o.CAFile) {
		return o, ErrInvalid
	}
	return o, nil
}

func normalizeElasticsearchProfile(p Profile) (Profile, error) {
	var err error
	p.ElasticsearchOptions, err = NormalizeElasticsearchOptions(p.ElasticsearchOptions)
	if err != nil {
		return p, err
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	_, err = ElasticsearchProfileConfig(p)
	return p, err
}

func ElasticsearchProfileConfig(p Profile) (ElasticsearchConfig, error) {
	if p.Kind != Elasticsearch || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Port == 0 || p.Host == "" || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "verify-full" && p.TLSMode != "disable" {
		return ElasticsearchConfig{}, ErrInvalid
	}
	o, err := NormalizeElasticsearchOptions(p.ElasticsearchOptions)
	if err != nil {
		return ElasticsearchConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeElasticsearchConfig(ElasticsearchConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Auth: o.Auth, Username: p.Username, CAFile: o.CAFile, ServerName: o.ServerName})
}

func parseElasticsearchURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Elasticsearch)
	if u.Path != "" && u.Path != "/" || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p.Host = u.Hostname()
	if raw := u.Port(); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		switch key {
		case "auth":
			p.ElasticsearchOptions.Auth = values[0]
		case "sslmode":
			p.TLSMode = values[0]
		case "ca_file":
			p.ElasticsearchOptions.CAFile = values[0]
		case "server_name":
			p.ElasticsearchOptions.ServerName = values[0]
		default:
			return fail()
		}
	}
	if p.ElasticsearchOptions.Auth != "basic" {
		p.Username = ""
	}
	secret := ""
	if u.User != nil {
		if p.ElasticsearchOptions.Auth == "none" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "Elasticsearch · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := ElasticsearchProfileConfig(p)
	if err != nil || ValidateElasticsearchCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
