package connections

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Database is the optional default catalog; schema is a separate public option.
// These defaults never constrain the qualified catalog/schema navigator.
type TrinoOptions struct {
	Version                     int    `json:"version"`
	Mode                        string `json:"mode,omitempty"`
	DriverVersion               string `json:"driver_version,omitempty"`
	DriverSHA256                string `json:"driver_sha256,omitempty"`
	DriverDirectory             string `json:"driver_directory,omitempty"`
	ConnectTimeoutSeconds       int    `json:"connect_timeout_seconds,omitempty"`
	QueryTimeoutSeconds         int    `json:"query_timeout_seconds,omitempty"`
	PoolWaitTimeoutMilliseconds int    `json:"pool_wait_timeout_milliseconds,omitempty"`
	MaxConnections              int    `json:"max_connections,omitempty"`
	MetadataReserve             int    `json:"metadata_reserve,omitempty"`
	SQLMaxConnections           int    `json:"sql_max_connections,omitempty"`
	DefaultSchema               string `json:"default_schema,omitempty"`
	CAFile                      string `json:"ca_file,omitempty"`
	ServerName                  string `json:"server_name,omitempty"`
}

func NormalizeTrinoOptions(a TrinoOptions) (TrinoOptions, error) {
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Mode == "" {
		a.Mode = "http"
	}
	if a.MaxConnections == 0 {
		a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections = 8, 2, 6
	}
	if a.SQLMaxConnections == 0 {
		a.SQLMaxConnections = a.MaxConnections - a.MetadataReserve
	}
	if a.ConnectTimeoutSeconds == 0 {
		a.ConnectTimeoutSeconds = 15
	}
	if a.PoolWaitTimeoutMilliseconds == 0 {
		a.PoolWaitTimeoutMilliseconds = 30000
	}
	invalid := func() (TrinoOptions, error) { return a, fmt.Errorf("%w: invalid Trino driver settings", ErrInvalid) }
	if a.Version != 1 || a.Mode != "http" && a.Mode != "jdbc" || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 300 || a.QueryTimeoutSeconds < 0 || a.QueryTimeoutSeconds > 86400 || a.PoolWaitTimeoutMilliseconds < 250 || a.PoolWaitTimeoutMilliseconds > 300000 || a.MaxConnections < 1 || a.MaxConnections > 32 || a.MetadataReserve < 0 || a.MetadataReserve >= a.MaxConnections || a.SQLMaxConnections < 1 || a.SQLMaxConnections > a.MaxConnections-a.MetadataReserve {
		return invalid()
	}
	if a.DriverDirectory != "" && (!filepath.IsAbs(a.DriverDirectory) || len(a.DriverDirectory) > 4096 || !utf8.ValidString(a.DriverDirectory) || strings.IndexFunc(a.DriverDirectory, unicode.IsControl) >= 0) {
		return invalid()
	}
	if a.DriverVersion != "" || a.DriverSHA256 != "" {
		if !h2VersionName.MatchString(a.DriverVersion) || len(a.DriverSHA256) != 64 || strings.ToLower(a.DriverSHA256) != a.DriverSHA256 {
			return invalid()
		}
		if _, err := hex.DecodeString(a.DriverSHA256); err != nil {
			return invalid()
		}
	}
	return a, nil
}

func normalizeTrinoProfile(p Profile) (Profile, error) {
	for _, name := range []string{p.Database, p.TrinoOptions.DefaultSchema} {
		if len(name) > 256 {
			return p, fmt.Errorf("%w: Trino catalog or schema exceeds 256 bytes", ErrInvalid)
		}
	}
	a, optionErr := NormalizeTrinoOptions(p.TrinoOptions)
	p.TrinoOptions = a
	if optionErr != nil {
		return p, optionErr
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	if p.TrinoOptions.Version != 1 || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return p, fmt.Errorf("%w: invalid Trino profile", ErrInvalid)
	}
	_, err := TrinoProfileConfig(p)
	return p, err
}

func TrinoProfileConfig(p Profile) (TrinoConfig, error) {
	if p.Kind != Trino {
		return TrinoConfig{}, ErrInvalid
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeTrinoConfig(TrinoConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Username: p.Username, Catalog: p.Database, Schema: p.TrinoOptions.DefaultSchema, CAFile: p.TrinoOptions.CAFile, ServerName: p.TrinoOptions.ServerName})
}

func parseTrinoURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Trino)
	p.Host = u.Hostname()
	if p.Host == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	if raw := u.Port(); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 16)
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
	if path != "" {
		parts := strings.Split(path, "/")
		if len(parts) > 2 {
			return fail()
		}
		var err error
		p.Database, err = url.PathUnescape(parts[0])
		if err != nil || p.Database == "" {
			return fail()
		}
		if len(parts) == 2 {
			p.TrinoOptions.DefaultSchema, err = url.PathUnescape(parts[1])
			if err != nil || p.TrinoOptions.DefaultSchema == "" {
				return fail()
			}
		}
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for k, v := range options {
		if len(v) != 1 || v[0] == "" {
			return fail()
		}
		switch k {
		case "sslmode":
			p.TLSMode = v[0]
		case "schema":
			if p.TrinoOptions.DefaultSchema != "" {
				return fail()
			}
			p.TrinoOptions.DefaultSchema = v[0]
		case "ca_file":
			p.TrinoOptions.CAFile = v[0]
		case "server_name":
			p.TrinoOptions.ServerName = v[0]
		case "mode":
			p.TrinoOptions.Mode = v[0]
		case "driver_version":
			p.TrinoOptions.DriverVersion = v[0]
		case "driver_sha256":
			p.TrinoOptions.DriverSHA256 = v[0]
		case "driver_directory":
			p.TrinoOptions.DriverDirectory = v[0]
		default:
			return fail()
		}
	}
	if p.TLSMode == "disable" && password != "" {
		return fail()
	}
	for _, v := range []string{p.Database, p.TrinoOptions.DefaultSchema} {
		if len(v) > 256 || !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fail()
		}
	}
	p.Name = "Trino · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: password}, nil
}
