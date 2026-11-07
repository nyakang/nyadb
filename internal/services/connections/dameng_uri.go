package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseDamengURI(u *url.URL) (URIImport, error) {
	fail := func(err error) (URIImport, error) { return URIImport{}, err }
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail(ErrURI)
	}
	if strings.Contains(u.Hostname(), ":") && net.ParseIP(u.Hostname()) == nil {
		return fail(ErrURI)
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if path == "" || strings.Contains(path, "/") {
		return fail(ErrURI)
	}
	database, err := url.PathUnescape(path)
	if err != nil {
		return fail(ErrURI)
	}
	p, _ := Defaults(Dameng)
	p.Host, p.Database, p.Username = u.Hostname(), database, u.User.Username()
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return fail(ErrURI)
		}
		p.Port = uint16(n)
	}
	password, _ := u.User.Password()
	if len(password) > 512 || !utf8.ValidString(password) || strings.IndexFunc(password, unicode.IsControl) >= 0 {
		return fail(ErrURI)
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail(ErrURI)
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail(ErrURIOptions)
		}
		value := values[0]
		switch key {
		case "sslmode":
			p.TLSMode = value
		case "library_file":
			p.DamengOptions.LibraryFile = value
		case "schema":
			p.DamengOptions.DefaultSchema = value
		case "ca_file":
			p.DamengOptions.CAFile = value
		case "certificate_file":
			p.DamengOptions.CertificateFile = value
		case "key_file":
			p.DamengOptions.KeyFile = value
		case "server_name":
			p.DamengOptions.ServerName = value
		case "connect_timeout_seconds", "operation_timeout_seconds", "max_connections", "sql_max_connections":
			n, err := strconv.Atoi(value)
			if err != nil || strconv.Itoa(n) != value {
				return fail(ErrURIOptions)
			}
			switch key {
			case "connect_timeout_seconds":
				p.DamengOptions.ConnectTimeoutSeconds = n
			case "operation_timeout_seconds":
				p.DamengOptions.OperationTimeoutSeconds = n
			case "max_connections":
				p.DamengOptions.MaxConnections = n
			case "sql_max_connections":
				p.DamengOptions.SQLMaxConnections = n
			}
		default:
			return fail(ErrURIOptions)
		}
	}
	p.Name = "Dameng · " + p.Host + " · " + p.Database
	if r := []rune(p.Name); len(r) > 120 {
		p.Name = string(r[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail(ErrURI)
	}
	return URIImport{Profile: p, Password: password}, nil
}
