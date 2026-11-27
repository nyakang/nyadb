package connections

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseDatabendURI(u *url.URL, jdbc bool) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Databend)
	p.Host = u.Hostname()
	if p.Host == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	canonical := make(map[string]string, len(options))
	for key, values := range options {
		if strings.EqualFold(key, "ssl") || strings.EqualFold(key, "sslmode") {
			key = strings.ToLower(key)
		}
		if _, duplicate := canonical[key]; duplicate || len(values) != 1 {
			return fail()
		}
		canonical[key] = values[0]
	}
	tls := !jdbc
	ssl, hasSSL := canonical["ssl"]
	mode, hasMode := canonical["sslmode"]
	ssl, mode = strings.ToLower(strings.TrimSpace(ssl)), strings.ToLower(strings.TrimSpace(mode))
	if hasSSL && ssl != "true" && ssl != "false" || hasMode && mode != "enable" && mode != "require" && mode != "disable" ||
		hasSSL && hasMode && (ssl == "true") != (mode != "disable") {
		return fail()
	}
	if hasSSL {
		tls = ssl == "true"
	} else if hasMode {
		tls = mode != "disable"
	}
	// Native DSNs default to HTTPS; JDBC defaults to HTTP on its own port.
	// An explicit non-TLS native DSN uses HTTP's default, not the chooser's.
	p.Port, p.TLSMode = 80, "disable"
	if tls {
		p.Port, p.TLSMode = 443, "verify-full"
	} else if jdbc {
		p.Port = 8000
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if strings.Contains(path, "/") {
		return fail()
	}
	if path != "" {
		p.Database, err = url.PathUnescape(path)
		if err != nil {
			return fail()
		}
	}
	for key, value := range canonical {
		switch key {
		case "ssl", "sslmode":
		case "user", "password":
			if u.User != nil {
				return fail()
			}
			if key == "user" {
				p.Username = value
			} else {
				password = value
			}
		case "auth":
			p.DatabendOptions.Auth = value
		case "ca_file":
			p.DatabendOptions.CAFile = value
		case "server_name":
			p.DatabendOptions.ServerName = value
		case "client_cert_file":
			p.DatabendOptions.ClientCertFile = value
		case "client_key_file":
			p.DatabendOptions.ClientKeyFile = value
		case "driver_version":
			p.DatabendOptions.DriverVersion = value
		case "driver_sha256":
			p.DatabendOptions.DriverSHA256 = value
		case "driver_directory":
			p.DatabendOptions.DriverDirectory = value
		default:
			// Do not silently discard cloud warehouse, routing or JDBC properties.
			return fail()
		}
	}
	if p.DatabendOptions.Auth != "basic" && u.User == nil {
		if _, explicit := canonical["user"]; !explicit {
			p.Username = ""
		}
	}
	if p.DatabendOptions.Auth == "none" && password != "" {
		return fail()
	}
	if !utf8.ValidString(password) || strings.IndexFunc(password, unicode.IsControl) >= 0 {
		return fail()
	}
	p.Name = "Databend · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: password}, nil
}
