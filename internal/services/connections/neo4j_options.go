package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Neo4jOptions holds native public settings, never a password or userinfo URI.
// Protocol is explicit direct Bolt or native Neo4j routing. RoutingContext is
// an encoded native query string; Database/Username remain exact profile fields.
type Neo4jOptions struct {
	Version               int    `json:"version"`
	Protocol              string `json:"protocol"`
	Auth                  string `json:"auth"`
	RoutingContext        string `json:"routing_context"`
	CAFile                string `json:"ca_file,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

func DefaultNeo4jOptions() Neo4jOptions {
	return Neo4jOptions{Version: 1, Protocol: "bolt", Auth: "basic", ConnectTimeoutSeconds: 10, RequestTimeoutSeconds: 40}
}
func NormalizeNeo4jOptions(o Neo4jOptions) (Neo4jOptions, error) {
	if o == (Neo4jOptions{}) {
		o = DefaultNeo4jOptions()
	}
	if o.Version != 1 || o.Protocol != "bolt" && o.Protocol != "neo4j" || o.Auth != "basic" && o.Auth != "none" || len(o.RoutingContext) > 8192 || strings.ContainsAny(o.RoutingContext, "?#") {
		return o, ErrInvalid
	}
	uri := o.Protocol + "+s://localhost:7687"
	if o.RoutingContext != "" {
		uri += "?" + o.RoutingContext
	}
	c, e := NormalizeNeo4jConfig(Neo4jConfig{URI: uri, Auth: o.Auth, Username: func() string {
		if o.Auth == "basic" {
			return "neo4j"
		}
		return ""
	}(), CAFile: o.CAFile, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
	if e != nil {
		return o, e
	}
	u, _ := url.Parse(c.URI)
	o.RoutingContext = u.RawQuery
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = c.ConnectTimeoutSeconds, c.RequestTimeoutSeconds
	return o, nil
}

func Neo4jProfileConfig(p Profile) (Neo4jConfig, error) {
	if p.Kind != Neo4j || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Host == "" || p.Port == 0 || strings.ContainsAny(p.Host, "[]@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
		return Neo4jConfig{}, ErrInvalid
	}
	o, e := NormalizeNeo4jOptions(p.Neo4jOptions)
	if e != nil {
		return Neo4jConfig{}, e
	}
	scheme := o.Protocol
	switch p.TLSMode {
	case "disable":
	case "verify-full":
		scheme += "+s"
	case "require":
		scheme += "+ssc"
	default:
		return Neo4jConfig{}, ErrInvalid
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), RawQuery: o.RoutingContext}
	return NormalizeNeo4jConfig(Neo4jConfig{URI: u.String(), Auth: o.Auth, Username: p.Username, Database: p.Database, CAFile: o.CAFile, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ConnectTimeoutSeconds: o.ConnectTimeoutSeconds, RequestTimeoutSeconds: o.RequestTimeoutSeconds})
}
func normalizeNeo4jProfile(p Profile) (Profile, error) {
	o, e := NormalizeNeo4jOptions(p.Neo4jOptions)
	if e != nil {
		return p, e
	}
	p.Neo4jOptions = o
	c, e := Neo4jProfileConfig(p)
	if e != nil {
		return p, e
	}
	p.Database = c.Database
	return p, nil
}

// ParseNeo4jURI is pure. Native schemes retain direct/routing/TLS semantics.
// Userinfo secrets become temporary credentials. Optional database paths and
// NyaDB configuration parameters never enter the driver's native routing URI.
func ParseNeo4jURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail()
	}
	u, e := url.Parse(raw)
	if e != nil || u.Opaque != "" || u.Fragment != "" || strings.ContainsRune(raw, '#') || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") || strings.Count(u.EscapedPath(), "/") > 1 {
		return fail()
	}
	p, _ := Defaults(Neo4j)
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "bolt", "neo4j":
		p.TLSMode = "disable"
	case "bolt+s", "neo4j+s":
		p.TLSMode = "verify-full"
	case "bolt+ssc", "neo4j+ssc":
		p.TLSMode = "require"
	default:
		return fail()
	}
	p.Neo4jOptions.Protocol = strings.Split(scheme, "+")[0]
	p.Host = u.Hostname()
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	pathDatabase := u.Path != "" && u.Path != "/"
	if pathDatabase {
		p.Database = strings.TrimPrefix(u.Path, "/")
	}
	secret := ""
	if u.User != nil {
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
		if p.Username == "" {
			return fail()
		}
	}
	options, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return fail()
	}
	routing := url.Values{}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		value := values[0]
		switch key {
		case "auth":
			p.Neo4jOptions.Auth = value
		case "database":
			if pathDatabase {
				return fail()
			}
			p.Database = value
		case "sslmode":
			if value != p.TLSMode {
				return fail()
			}
		case "server_name":
			return fail()
		case "ca_file":
			p.Neo4jOptions.CAFile = value
		case "client_cert_file":
			p.Neo4jOptions.ClientCertFile = value
		case "client_key_file":
			p.Neo4jOptions.ClientKeyFile = value
		case "connect_timeout", "request_timeout":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || strconv.Itoa(n) != value {
				return fail()
			}
			if key == "connect_timeout" {
				p.Neo4jOptions.ConnectTimeoutSeconds = n
			} else {
				p.Neo4jOptions.RequestTimeoutSeconds = n
			}
		default:
			routing[key] = values
		}
	}
	if p.Neo4jOptions.Auth == "none" {
		if u.User != nil {
			return fail()
		}
		p.Username = ""
	}
	p.Neo4jOptions.RoutingContext = routing.Encode()
	p.Name = "Neo4j · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, e = NormalizeProfile(p)
	if e != nil {
		return fail()
	}
	c, e := Neo4jProfileConfig(p)
	if e != nil || ValidateNeo4jDraftCredential(c, secret) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
