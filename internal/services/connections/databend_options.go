package connections

import (
	"encoding/hex"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// DatabendOptions retains original target and package settings independently
// of ephemeral passwords/tokens. A saved unselected package never resolves to
// an implicit latest driver at connect time.
type DatabendOptions struct {
	Version               int    `json:"version"`
	Auth                  string `json:"auth,omitempty"`
	DriverVersion         string `json:"driver_version,omitempty"`
	DriverSHA256          string `json:"driver_sha256,omitempty"`
	DriverDirectory       string `json:"driver_directory,omitempty"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
	ClientCertFile        string `json:"client_cert_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds,omitempty"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds,omitempty"`
	QueryTimeoutSeconds   int    `json:"query_timeout_seconds,omitempty"`
	PoolWaitMillis        int    `json:"pool_wait_millis,omitempty"`
	MaxConnections        int    `json:"max_connections,omitempty"`
	MetadataReserve       int    `json:"metadata_reserve,omitempty"`
	SQLMaxConnections     int    `json:"sql_max_connections,omitempty"`
}

func DefaultDatabendOptions() DatabendOptions {
	a, _ := NormalizeDatabendOptions(DatabendOptions{})
	return a
}

func NormalizeDatabendOptions(a DatabendOptions) (DatabendOptions, error) {
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Auth == "" {
		a.Auth = "basic"
	}
	if a.ConnectTimeoutSeconds == 0 {
		a.ConnectTimeoutSeconds = 30
	}
	if a.RequestTimeoutSeconds == 0 {
		a.RequestTimeoutSeconds = 120
	}
	if a.MaxConnections == 0 {
		a.MaxConnections = 4
	}
	if a.MetadataReserve == 0 {
		a.MetadataReserve = 1
	}
	if a.SQLMaxConnections == 0 {
		a.SQLMaxConnections = a.MaxConnections - a.MetadataReserve
	}
	if a.PoolWaitMillis == 0 {
		a.PoolWaitMillis = 5000
	}
	if a.Version != 1 || a.Auth != "basic" && a.Auth != "bearer" && a.Auth != "none" ||
		a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.RequestTimeoutSeconds < 1 || a.RequestTimeoutSeconds > 3600 ||
		a.QueryTimeoutSeconds < 0 || a.QueryTimeoutSeconds > 86400 || a.MaxConnections < 2 || a.MaxConnections > 32 ||
		a.MetadataReserve < 1 || a.MetadataReserve >= a.MaxConnections || a.SQLMaxConnections < 1 || a.SQLMaxConnections > a.MaxConnections-a.MetadataReserve ||
		a.PoolWaitMillis < 250 || a.PoolWaitMillis > 300000 {
		return a, ErrInvalid
	}
	if a.DriverVersion != "" || a.DriverSHA256 != "" {
		if !databendDriverVersion.MatchString(a.DriverVersion) || len(a.DriverSHA256) != 64 || strings.ToLower(a.DriverSHA256) != a.DriverSHA256 {
			return a, ErrInvalid
		}
		if _, err := hex.DecodeString(a.DriverSHA256); err != nil {
			return a, ErrInvalid
		}
	}
	for _, path := range []string{a.DriverDirectory, a.CAFile, a.ClientCertFile, a.ClientKeyFile} {
		if path != "" && (!filepath.IsAbs(path) || !validEngineText(path, 4096)) {
			return a, ErrInvalid
		}
	}
	if a.ServerName != "" && !validEngineText(a.ServerName, 256) {
		return a, ErrInvalid
	}
	return a, nil
}

func normalizeDatabendProfile(p Profile) (Profile, error) {
	a, err := NormalizeDatabendOptions(p.DatabendOptions)
	p.DatabendOptions = a
	if err != nil || p.Host == "" || p.Port == 0 || !validEngineText(p.Host, 256) ||
		strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil ||
		!validEngineText(p.Database, 256) || p.Database == "" || p.MySQLAdvanced != (MySQLAdvanced{}) {
		return p, ErrInvalid
	}
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	if p.TLSMode != "disable" && p.TLSMode != "verify-full" {
		return p, ErrInvalid
	}
	_, err = DatabendProfileTransport(p)
	return p, err
}

func DatabendProfileTransport(p Profile) (DatabendConfig, error) {
	if p.Kind != Databend {
		return DatabendConfig{}, ErrInvalid
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	a := p.DatabendOptions
	return NormalizeDatabendConfig(DatabendConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))),
		Auth: a.Auth, Username: p.Username, CAFile: a.CAFile, ServerName: a.ServerName,
		ClientCertFile: a.ClientCertFile, ClientKeyFile: a.ClientKeyFile,
		ConnectTimeoutSeconds: a.ConnectTimeoutSeconds, RequestTimeoutSeconds: a.RequestTimeoutSeconds})
}

func DatabendProfileManagedConfig(p Profile) (DatabendManagedConfig, error) {
	p, err := NormalizeProfile(p)
	if err != nil || p.Kind != Databend {
		return DatabendManagedConfig{}, ErrInvalid
	}
	c, err := DatabendProfileTransport(p)
	if err != nil {
		return DatabendManagedConfig{}, err
	}
	a := p.DatabendOptions
	return NormalizeDatabendManagedConfig(DatabendManagedConfig{Transport: c, Database: p.Database,
		DriverVersion: a.DriverVersion, DriverSHA256: a.DriverSHA256,
		MaxConnections: a.MaxConnections, MetadataReserve: a.MetadataReserve, PoolWaitMillis: a.PoolWaitMillis})
}
