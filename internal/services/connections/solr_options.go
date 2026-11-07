package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// SolrOptions contains only versioned public settings. ContextPath identifies
// the HTTP service context, never a selected core/collection or SQL database.
type SolrOptions struct {
	Version     int    `json:"version"`
	Auth        string `json:"auth"`
	ContextPath string `json:"context_path"`
	CAFile      string `json:"ca_file,omitempty"`
	ServerName  string `json:"server_name,omitempty"`
}

func DefaultSolrOptions() SolrOptions {
	return SolrOptions{Version: 1, Auth: "none", ContextPath: "/solr"}
}

func NormalizeSolrOptions(o SolrOptions) (SolrOptions, error) {
	if o == (SolrOptions{}) {
		o = DefaultSolrOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" || o.ContextPath != "" && !strings.HasPrefix(o.ContextPath, "/") {
		return o, ErrInvalid
	}
	// Reuse the native context and verified TLS validation without interpreting
	// the path as a core or retaining any userinfo-bearing endpoint.
	c, err := NormalizeSolrConfig(SolrConfig{Endpoint: "https://localhost:8983" + o.ContextPath, CAFile: o.CAFile, ServerName: o.ServerName})
	if err != nil {
		return o, err
	}
	o.ContextPath = strings.TrimPrefix(c.Endpoint, "https://localhost:8983")
	return o, nil
}

func normalizeSolrProfile(p Profile) (Profile, error) {
	var err error
	p.SolrOptions, err = NormalizeSolrOptions(p.SolrOptions)
	if err != nil {
		return p, err
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	_, err = SolrProfileConfig(p)
	return p, err
}

func SolrProfileConfig(p Profile) (SolrConfig, error) {
	if p.Kind != Solr || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Port == 0 || p.Host == "" || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "verify-full" && p.TLSMode != "disable" {
		return SolrConfig{}, ErrInvalid
	}
	o, err := NormalizeSolrOptions(p.SolrOptions)
	if err != nil {
		return SolrConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeSolrConfig(SolrConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))) + o.ContextPath, Auth: o.Auth, Username: p.Username, CAFile: o.CAFile, ServerName: o.ServerName})
}

func parseSolrURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Solr)
	if u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p.Host, p.SolrOptions.ContextPath = u.Hostname(), u.Path
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
	if u.User != nil {
		p.SolrOptions.Auth = "basic"
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		switch key {
		case "auth":
			p.SolrOptions.Auth = values[0]
		case "sslmode":
			p.TLSMode = values[0]
		case "ca_file":
			p.SolrOptions.CAFile = values[0]
		case "server_name":
			p.SolrOptions.ServerName = values[0]
		default:
			return fail()
		}
	}
	secret := ""
	if u.User != nil {
		if p.SolrOptions.Auth != "basic" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "Apache Solr · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := SolrProfileConfig(p)
	if err != nil || ValidateSolrCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
