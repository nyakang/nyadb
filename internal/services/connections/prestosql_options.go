package connections

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// PrestoSQLOptions contains public settings only. Driver pins remain explicit;
// authentication and store passwords belong to the credential vault.
type PrestoSQLOptions struct {
	Version               int    `json:"version"`
	DefaultSchema         string `json:"default_schema,omitempty"`
	JDBCParameters        string `json:"jdbc_parameters,omitempty"`
	DriverVersion         string `json:"driver_version,omitempty"`
	DriverSHA256          string `json:"driver_sha256,omitempty"`
	DriverDirectory       string `json:"driver_directory,omitempty"`
	TrustStorePath        string `json:"trust_store_path,omitempty"`
	TrustStoreType        string `json:"trust_store_type,omitempty"`
	KeyStorePath          string `json:"key_store_path,omitempty"`
	KeyStoreType          string `json:"key_store_type,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds,omitempty"`
	QueryTimeoutSeconds   int    `json:"query_timeout_seconds,omitempty"`
	MaxQueryProcesses     int    `json:"max_query_processes,omitempty"`
	PoolWaitMillis        int    `json:"pool_wait_millis,omitempty"`
}

func DefaultPrestoSQLOptions() PrestoSQLOptions {
	a, _ := NormalizePrestoSQLOptions(PrestoSQLOptions{})
	return a
}

func NormalizePrestoSQLOptions(a PrestoSQLOptions) (PrestoSQLOptions, error) {
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Version != 1 {
		return a, ErrInvalid
	}
	for _, path := range []string{a.DriverDirectory, a.TrustStorePath, a.KeyStorePath} {
		if path != "" && (!filepath.IsAbs(path) || !prestoConfigText(path, 4096)) {
			return a, ErrInvalid
		}
	}
	for _, kind := range []string{a.TrustStoreType, a.KeyStoreType} {
		if kind != "" && kind != "JKS" && kind != "PKCS12" {
			return a, ErrInvalid
		}
	}
	params, err := url.ParseQuery(a.JDBCParameters)
	if err != nil {
		return a, ErrInvalid
	}
	for _, key := range []string{"SSLVerification", "SSLTrustStorePath", "SSLTrustStoreType", "SSLKeyStorePath", "SSLKeyStoreType"} {
		if _, ok := params[key]; ok {
			return a, ErrInvalid
		}
	}
	// Unselected packages can be saved as drafts, but never opened implicitly.
	version, digest := a.DriverVersion, a.DriverSHA256
	if version == "" && digest == "" {
		version = "unselected"
		digest = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	c, err := NormalizePrestoSQLManagedConfig(PrestoSQLManagedConfig{Endpoint: "http://localhost:8080", Username: "validation", Catalog: "validation", Schema: a.DefaultSchema, JDBCParameters: a.JDBCParameters, DriverVersion: version, DriverSHA256: digest, ConnectTimeoutSeconds: a.ConnectTimeoutSeconds, QueryTimeoutSeconds: a.QueryTimeoutSeconds, MaxQueryProcesses: a.MaxQueryProcesses, PoolWaitMillis: a.PoolWaitMillis})
	if err != nil {
		return a, err
	}
	a.JDBCParameters = c.JDBCParameters
	a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.MaxQueryProcesses, a.PoolWaitMillis = c.ConnectTimeoutSeconds, c.QueryTimeoutSeconds, c.MaxQueryProcesses, c.PoolWaitMillis
	return a, nil
}

func normalizePrestoSQLProfile(p Profile) (Profile, error) {
	a, err := NormalizePrestoSQLOptions(p.PrestoSQLOptions)
	p.PrestoSQLOptions = a
	if err != nil || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Host == "" || p.Port == 0 || !prestoConfigText(p.Host, 256) || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
		return p, ErrInvalid
	}
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	if p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-ca" && p.TLSMode != "verify-full" {
		return p, ErrInvalid
	}
	if p.TLSMode == "disable" && (a.TrustStorePath != "" || a.TrustStoreType != "" || a.KeyStorePath != "" || a.KeyStoreType != "") {
		return p, ErrInvalid
	}
	c := prestoProfileConfig(p)
	if c.DriverVersion == "" && c.DriverSHA256 == "" {
		c.DriverVersion = "unselected"
		c.DriverSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	_, err = NormalizePrestoSQLManagedConfig(c)
	return p, err
}

func prestoProfileConfig(p Profile) PrestoSQLManagedConfig {
	a := p.PrestoSQLOptions
	params, _ := url.ParseQuery(a.JDBCParameters)
	scheme := "http"
	if p.TLSMode != "disable" {
		scheme = "https"
		verification := map[string]string{"require": "NONE", "verify-ca": "CA", "verify-full": "FULL"}[p.TLSMode]
		params.Set("SSLVerification", verification)
		for key, value := range map[string]string{"SSLTrustStorePath": a.TrustStorePath, "SSLTrustStoreType": a.TrustStoreType, "SSLKeyStorePath": a.KeyStorePath, "SSLKeyStoreType": a.KeyStoreType} {
			if value != "" {
				params.Set(key, value)
			}
		}
	}
	return PrestoSQLManagedConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Username: p.Username, Catalog: p.Database, Schema: a.DefaultSchema, JDBCParameters: params.Encode(), DriverVersion: a.DriverVersion, DriverSHA256: a.DriverSHA256, ConnectTimeoutSeconds: a.ConnectTimeoutSeconds, QueryTimeoutSeconds: a.QueryTimeoutSeconds, MaxQueryProcesses: a.MaxQueryProcesses, PoolWaitMillis: a.PoolWaitMillis}
}

func PrestoSQLProfileManagedConfig(p Profile) (PrestoSQLManagedConfig, error) {
	if p.Kind != PrestoSQL {
		return PrestoSQLManagedConfig{}, ErrInvalid
	}
	p, err := NormalizeProfile(p)
	if err != nil {
		return PrestoSQLManagedConfig{}, err
	}
	return NormalizePrestoSQLManagedConfig(prestoProfileConfig(p))
}
