package connections

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PostgreSQLOptions contains public settings only. Certificate paths are
// metadata; passwords and key contents remain in separate credential stores.
type PostgreSQLOptions struct {
	ToolsDirectory        string      `json:"tools_directory,omitempty"`
	Version               int         `json:"version"`
	ConnectTimeoutSeconds int         `json:"connect_timeout_seconds"`
	MaxConnections        int         `json:"max_connections"`
	SQLMaxConnections     int         `json:"sql_max_connections,omitempty"`
	ApplicationName       string      `json:"application_name,omitempty"`
	DefaultSchema         string      `json:"default_schema,omitempty"`
	StatementTimeoutMS    int         `json:"statement_timeout_ms,omitempty"`
	LockTimeoutMS         int         `json:"lock_timeout_ms,omitempty"`
	CAFile                string      `json:"ca_file,omitempty"`
	CertificateFile       string      `json:"certificate_file,omitempty"`
	KeyFile               string      `json:"key_file,omitempty"`
	SSH                   SSHOptions  `json:"ssh,omitempty"`
	HTTP                  HTTPOptions `json:"http,omitempty"`
}

func DefaultPostgreSQLOptions() PostgreSQLOptions {
	return PostgreSQLOptions{Version: 1, ConnectTimeoutSeconds: 10, MaxConnections: 8, SQLMaxConnections: 8, ApplicationName: "NyaDB", SSH: DefaultMySQLSSH(), HTTP: DefaultMySQLHTTP()}
}
func NormalizePostgreSQLOptions(a PostgreSQLOptions) (PostgreSQLOptions, error) {
	if a == (PostgreSQLOptions{}) {
		a = DefaultPostgreSQLOptions()
	}
	if a.SQLMaxConnections == 0 {
		a.SQLMaxConnections = 8 // profiles saved before independent SQL pools
	}
	if a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return a, fmt.Errorf("%w: invalid PostgreSQL SQL connection limit", ErrInvalid)
	}
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.StatementTimeoutMS < 0 || a.StatementTimeoutMS > 86400000 || a.LockTimeoutMS < 0 || a.LockTimeoutMS > 86400000 {
		return a, fmt.Errorf("%w: invalid PostgreSQL timeouts or pool limit", ErrInvalid)
	}
	for _, v := range []string{a.ToolsDirectory, a.ApplicationName, a.DefaultSchema, a.CAFile, a.CertificateFile, a.KeyFile} {
		if !validEngineText(v, 4096) {
			return a, fmt.Errorf("%w: invalid PostgreSQL option text", ErrInvalid)
		}
	}
	if len([]rune(a.ApplicationName)) > 63 || len([]byte(a.DefaultSchema)) > 63 {
		return a, fmt.Errorf("%w: PostgreSQL application/schema name exceeds server limit", ErrInvalid)
	}
	if (a.CertificateFile == "") != (a.KeyFile == "") {
		return a, fmt.Errorf("%w: PostgreSQL client certificate and key must be configured together", ErrInvalid)
	}
	var err error
	a.SSH, err = normalizeMySQLSSH(a.SSH)
	if err != nil {
		return a, err
	}
	a.HTTP, err = normalizeMySQLHTTP(a.HTTP)
	if err != nil {
		return a, err
	}
	if a.SSH.Enabled && a.HTTP.Enabled {
		return a, fmt.Errorf("%w: SSH and HTTP tunnels are mutually exclusive", ErrInvalid)
	}
	return a, nil
}

type SQLiteAttachment struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	ReadOnly bool   `json:"read_only,omitempty"`
}
type SQLiteOptions struct {
	Version           int                `json:"version"`
	MaxConnections    int                `json:"max_connections,omitempty"`
	SQLMaxConnections int                `json:"sql_max_connections,omitempty"`
	ReadOnly          bool               `json:"read_only,omitempty"`
	RequireExisting   bool               `json:"require_existing,omitempty"`
	BusyTimeoutMS     int                `json:"busy_timeout_ms"`
	ForeignKeys       bool               `json:"foreign_keys"`
	Attachments       []SQLiteAttachment `json:"attachments,omitempty"`
}

func DefaultSQLiteOptions() SQLiteOptions {
	return SQLiteOptions{Version: 1, MaxConnections: 8, SQLMaxConnections: 8, RequireExisting: true, BusyTimeoutMS: 1000, ForeignKeys: true}
}
func NormalizeSQLiteOptions(a SQLiteOptions) (SQLiteOptions, error) {
	// Missing legacy options retain the historical rw/create main-file behavior.
	if a.Version == 0 && a.MaxConnections == 0 && a.SQLMaxConnections == 0 && !a.ReadOnly && !a.RequireExisting && a.BusyTimeoutMS == 0 && !a.ForeignKeys && len(a.Attachments) == 0 {
		a = DefaultSQLiteOptions()
		a.RequireExisting = false
	}
	if a.MaxConnections == 0 {
		a.MaxConnections = 8
	}
	if a.SQLMaxConnections == 0 {
		a.SQLMaxConnections = 8
	}
	if a.MaxConnections < 1 || a.MaxConnections > 64 || a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return a, fmt.Errorf("%w: invalid SQLite connection limits", ErrInvalid)
	}
	a.Attachments = append([]SQLiteAttachment(nil), a.Attachments...)
	if a.Version != 1 || a.BusyTimeoutMS < 0 || a.BusyTimeoutMS > 3600000 || len(a.Attachments) > 10 {
		return a, fmt.Errorf("%w: invalid SQLite options or attachment limit", ErrInvalid)
	}
	names, files := map[string]bool{"main": true, "temp": true}, map[string]bool{}
	for i, v := range a.Attachments {
		v.Name = strings.TrimSpace(v.Name)
		v.File = strings.TrimSpace(v.File)
		if v.Name == "" || !validEngineText(v.Name, 128) || strings.HasPrefix(strings.ToLower(v.Name), "sqlite_") || names[strings.ToLower(v.Name)] || v.File == "" || !validEngineText(v.File, 4096) || !filepath.IsAbs(v.File) {
			return a, fmt.Errorf("%w: invalid or duplicate SQLite attachment name/path", ErrInvalid)
		}
		v.File = filepath.Clean(v.File)
		if files[v.File] {
			return a, fmt.Errorf("%w: duplicate SQLite attachment file", ErrInvalid)
		}
		names[strings.ToLower(v.Name)], files[v.File] = true, true
		a.Attachments[i] = v
	}
	return a, nil
}
func validEngineText(v string, maxBytes int) bool {
	return len(v) <= maxBytes && utf8.ValidString(v) && strings.IndexFunc(v, unicode.IsControl) < 0
}
