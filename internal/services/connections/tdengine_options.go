package connections

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TDengine options describe the native WebSocket session and its query leases.
type TDengineOptions struct {
	Version               int    `json:"version"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	QueryTimeoutSeconds   int    `json:"query_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	SQLMaxConnections     int    `json:"sql_max_connections"`
	ReadOnly              bool   `json:"read_only,omitempty"`
	CAFile                string `json:"ca_file,omitempty"`
}

func DefaultTDengineOptions() TDengineOptions {
	return TDengineOptions{Version: 1, ConnectTimeoutSeconds: 10, QueryTimeoutSeconds: 60, MaxConnections: 4, SQLMaxConnections: 4}
}

func NormalizeTDengineOptions(o TDengineOptions) (TDengineOptions, error) {
	if o.Version == 0 {
		d := DefaultTDengineOptions()
		if o.ConnectTimeoutSeconds == 0 {
			o.ConnectTimeoutSeconds = d.ConnectTimeoutSeconds
		}
		if o.QueryTimeoutSeconds == 0 {
			o.QueryTimeoutSeconds = d.QueryTimeoutSeconds
		}
		if o.MaxConnections == 0 {
			o.MaxConnections = d.MaxConnections
		}
		if o.SQLMaxConnections == 0 {
			o.SQLMaxConnections = d.SQLMaxConnections
		}
		o.Version = 1
	}
	if o.Version != 1 || o.ConnectTimeoutSeconds < 1 || o.ConnectTimeoutSeconds > 300 || o.QueryTimeoutSeconds < 1 || o.QueryTimeoutSeconds > 3600 || o.MaxConnections < 1 || o.MaxConnections > 64 || o.SQLMaxConnections < 1 || o.SQLMaxConnections > 32 || len(o.CAFile) > 4096 || !utf8.ValidString(o.CAFile) || strings.IndexFunc(o.CAFile, unicode.IsControl) >= 0 {
		return o, fmt.Errorf("%w: invalid TDengine options", ErrInvalid)
	}
	return o, nil
}

func normalizeTDengineProfile(p Profile) (Profile, error) {
	var err error
	p.TDengineOptions, err = NormalizeTDengineOptions(p.TDengineOptions)
	if err != nil {
		return p, err
	}
	p.Database = strings.ToLower(p.Database)
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	if (p.Database != "" && (len(p.Database) > 192 || !utf8.ValidString(p.Database) || strings.IndexFunc(p.Database, unicode.IsControl) >= 0 || strings.ContainsAny(p.Database, "`/\\"))) || p.Host == "" || len(p.Host) > 253 || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.Username == "" || len(p.Username) > 256 || !utf8.ValidString(p.Username) || strings.IndexFunc(p.Username, unicode.IsControl) >= 0 || p.MySQLAdvanced != (MySQLAdvanced{}) || p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-full" || p.TLSMode != "verify-full" && p.TDengineOptions.CAFile != "" {
		return p, fmt.Errorf("%w: invalid TDengine profile", ErrInvalid)
	}
	return p, nil
}

func parseTDengineURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(TDengine)
	secure := strings.EqualFold(u.Scheme, "wss") || strings.EqualFold(u.Scheme, "taoswss")
	if secure {
		p.TLSMode = "verify-full"
	}
	p.Host = u.Hostname()
	if strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	if port := u.Port(); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	if strings.Count(strings.TrimPrefix(u.Path, "/"), "/") != 0 {
		return fail()
	}
	p.Database = strings.TrimPrefix(u.Path, "/")
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		v := values[0]
		switch key {
		case "sslmode":
			p.TLSMode = v
		case "ca_file":
			p.TDengineOptions.CAFile = v
		case "read_only":
			if v != "true" && v != "false" {
				return fail()
			}
			p.TDengineOptions.ReadOnly = v == "true"
		case "connect_timeout", "query_timeout", "max_connections", "sql_max_connections":
			n, e := strconv.Atoi(v)
			if e != nil {
				return fail()
			}
			switch key {
			case "connect_timeout":
				p.TDengineOptions.ConnectTimeoutSeconds = n
			case "query_timeout":
				p.TDengineOptions.QueryTimeoutSeconds = n
			case "max_connections":
				p.TDengineOptions.MaxConnections = n
			case "sql_max_connections":
				p.TDengineOptions.SQLMaxConnections = n
			}
		default:
			return fail()
		}
	}
	p.Name = "TDengine · " + p.Host
	if secure && p.TLSMode == "disable" {
		return fail()
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: password}, nil
}
