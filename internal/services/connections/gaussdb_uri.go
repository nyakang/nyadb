package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// net/url assumes one host and rejects valid multi-host IPv6 authorities.
// Parse userinfo/path with a placeholder host, and validate endpoints separately.
func parseRawGaussDBURI(raw string) (URIImport, error) {
	_, rest, _ := strings.Cut(raw, ":")
	if !strings.HasPrefix(rest, "//") || strings.ContainsRune(raw, '#') {
		return URIImport{}, ErrURI
	}
	rest = rest[2:]
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	authority, suffix := rest[:end], rest[end:]
	hosts, userinfo := authority, ""
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userinfo, hosts = authority[:at+1], authority[at+1:]
	}
	u, err := url.Parse("gaussdb://" + userinfo + "placeholder" + suffix)
	if err != nil || u.Opaque != "" || u.Fragment != "" {
		return URIImport{}, ErrURI
	}
	endpoints, err := ParseGaussDBEndpoints(hosts, 5432)
	if err != nil {
		return URIImport{}, ErrURI
	}
	p, _ := Defaults(GaussDB)
	p.Host, p.Port = endpoints[0].Host, endpoints[0].Port
	var extra []string
	for _, endpoint := range endpoints[1:] {
		extra = append(extra, net.JoinHostPort(endpoint.Host, strconv.Itoa(int(endpoint.Port))))
	}
	p.GaussDBOptions.AdditionalEndpoints = strings.Join(extra, ",")
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if strings.Contains(path, "/") {
		return URIImport{}, ErrURI
	}
	if path != "" {
		p.Database, err = url.PathUnescape(path)
		if err != nil {
			return URIImport{}, ErrURI
		}
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return URIImport{}, ErrURI
	}
	for key, values := range options {
		if len(values) != 1 {
			return URIImport{}, ErrURIOptions
		}
		switch key {
		case "sslmode":
			p.TLSMode = values[0]
		case "target_session_attrs":
			p.GaussDBOptions.TargetSessionAttrs = values[0]
		default:
			return URIImport{}, ErrURIOptions
		}
	}
	for _, value := range []string{p.Database, p.Username, password} {
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return URIImport{}, ErrURI
		}
	}
	p.Name = "GaussDB · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return URIImport{}, ErrURI
	}
	return URIImport{Profile: p, Password: password}, nil
}
