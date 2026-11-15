package connections

import (
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// H2Options selects one immutable installed driver bundle and one database
// mode. Credentials and executable/JVM arguments are never profile fields.
type H2Options struct {
	Version                     int    `json:"version"`
	Mode                        string `json:"mode"`
	DriverProfile               string `json:"driver_profile"`
	DriverVersion               string `json:"driver_version,omitempty"`
	DriverSHA256                string `json:"driver_sha256,omitempty"`
	DriverDirectory             string `json:"driver_directory,omitempty"`
	DefaultSchema               string `json:"default_schema"`
	ConnectTimeoutSeconds       int    `json:"connect_timeout_seconds"`
	QueryTimeoutSeconds         int    `json:"query_timeout_seconds"`
	PoolWaitTimeoutMilliseconds int    `json:"pool_wait_timeout_milliseconds"`
	MaxConnections              int    `json:"max_connections"` // Physical JDBC pool, shared by SQL and features.
	MetadataReserve             int    `json:"metadata_reserve"`
	SQLMaxConnections           int    `json:"sql_max_connections"` // Logical SQL document limit within workload capacity.
	ReadOnly                    bool   `json:"read_only,omitempty"`
	RequireExisting             bool   `json:"require_existing,omitempty"`
	AutoServer                  bool   `json:"auto_server,omitempty"` // Mixed mode for writable local files only.
	TLSCAFile                   string `json:"tls_ca_file,omitempty"` // Optional PEM roots; TLS always verifies the original host.
}

func DefaultH2Options() H2Options {
	return H2Options{Version: 1, Mode: "file", DriverProfile: "h2-auto", DefaultSchema: "PUBLIC", ConnectTimeoutSeconds: 15, QueryTimeoutSeconds: 0, PoolWaitTimeoutMilliseconds: 30000, MaxConnections: 8, MetadataReserve: 2, SQLMaxConnections: 6, RequireExisting: true, AutoServer: true}
}

var h2VersionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// H2FileBasePath accepts current/legacy physical file names while retaining
// the JDBC database base path used by the native driver.
func H2FileBasePath(path string) string {
	path = strings.TrimSpace(path)
	for _, suffix := range []string{".mv.db", ".h2.db"} {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			return path[:len(path)-len(suffix)]
		}
	}
	return path
}

func NormalizeH2Options(a H2Options) (H2Options, error) {
	if a == (H2Options{}) {
		a = DefaultH2Options()
	}
	invalid := func() (H2Options, error) { return a, fmt.Errorf("%w: invalid H2 connection settings", ErrInvalid) }
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 300 || a.QueryTimeoutSeconds < 0 || a.QueryTimeoutSeconds > 86400 || a.MaxConnections < 1 || a.MaxConnections > 32 || a.MetadataReserve < 0 || a.MetadataReserve >= a.MaxConnections || a.SQLMaxConnections < 1 || a.SQLMaxConnections > a.MaxConnections-a.MetadataReserve {
		return invalid()
	}
	if a.PoolWaitTimeoutMilliseconds < 250 || a.PoolWaitTimeoutMilliseconds > 300000 {
		return invalid()
	}
	if a.Mode != "file" && a.Mode != "memory" && a.Mode != "tcp" && a.Mode != "ssl" {
		return invalid()
	}
	if a.DriverProfile != "h2-auto" && a.DriverProfile != "h2-custom" && a.DriverProfile != "h2-v1" && a.DriverProfile != "h2-v2" && a.DriverProfile != "h2-v3" && a.DriverProfile != "h2-legacy" {
		return invalid()
	}
	if a.DefaultSchema == "" || !utf8.ValidString(a.DefaultSchema) || len(a.DefaultSchema) > 4096 || strings.ContainsRune(a.DefaultSchema, 0) {
		return invalid()
	}
	if a.DriverDirectory != "" && (!filepath.IsAbs(a.DriverDirectory) || !utf8.ValidString(a.DriverDirectory) || len(a.DriverDirectory) > 4096 || strings.ContainsRune(a.DriverDirectory, 0)) {
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
	if a.ReadOnly && a.Mode != "file" {
		return invalid()
	}
	if a.AutoServer && (a.Mode != "file" || a.ReadOnly) {
		return invalid()
	}
	if a.TLSCAFile != "" && (a.Mode != "ssl" || !filepath.IsAbs(a.TLSCAFile) || !utf8.ValidString(a.TLSCAFile) || len(a.TLSCAFile) > 4096 || strings.ContainsAny(a.TLSCAFile, "\x00\r\n")) {
		return invalid()
	}
	if a.RequireExisting && a.Mode == "memory" {
		return invalid()
	}
	return a, nil
}

func normalizeH2Profile(p Profile) (Profile, error) {
	a, err := NormalizeH2Options(p.H2Options)
	p.H2Options = a
	if err != nil {
		return p, err
	}
	invalid := func() (Profile, error) { return p, fmt.Errorf("%w: invalid H2 database identity", ErrInvalid) }
	if !utf8.ValidString(p.Database) || len(p.Database) > 4096 || p.Database == "" || strings.ContainsAny(p.Database, ";\x00\r\n") || !utf8.ValidString(p.Username) || len(p.Username) > 4096 || strings.ContainsRune(p.Username, 0) || p.TLSMode != "" || p.MySQLAdvanced != (MySQLAdvanced{}) {
		return invalid()
	}
	if a.Mode == "file" {
		if !filepath.IsAbs(p.Database) || p.Host != "" || p.Port != 0 || strings.HasSuffix(strings.ToLower(p.Database), ".mv.db") || strings.HasSuffix(strings.ToLower(p.Database), ".h2.db") {
			return invalid()
		}
	} else if a.Mode == "memory" {
		if !h2VersionName.MatchString(p.Database) || p.Host != "" || p.Port != 0 {
			return invalid()
		}
	} else {
		if p.Host == "" || p.Port == 0 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n;") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || strings.HasPrefix(p.Database, "jdbc:") {
			return invalid()
		}
	}
	return p, nil
}
