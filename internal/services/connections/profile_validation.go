package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// NormalizeProfile validates public settings and applies their canonical values.
// Variants are normalized in place; callers retaining them must clone the slice.
func NormalizeProfile(p Profile) (Profile, error) {
	if !ValidProfileColor(p.Color) {
		return p, fmt.Errorf("%w: invalid connection color", ErrInvalid)
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Kind != DuckDB && p.DuckDBOptions != (DuckDBOptions{}) {
		return p, fmt.Errorf("%w: DuckDB options on another engine", ErrInvalid)
	}
	if p.Kind != SQLServer && p.SQLServerOptions != (SQLServerOptions{}) {
		return p, fmt.Errorf("%w: SQL Server options on another engine", ErrInvalid)
	}
	p.GroupName = strings.TrimSpace(p.GroupName)
	if p.GroupName != "" {
		if err := ValidateGroupName(p.GroupName); err != nil {
			return p, err
		}
	}
	if p.Kind != PostgreSQL && p.PostgreSQLOptions != (PostgreSQLOptions{}) {
		return p, fmt.Errorf("%w: PostgreSQL options on another engine", ErrInvalid)
	}
	if p.Kind != SQLite && (p.SQLiteOptions.Version != 0 || p.SQLiteOptions.MaxConnections != 0 || p.SQLiteOptions.SQLMaxConnections != 0 || p.SQLiteOptions.ReadOnly || p.SQLiteOptions.RequireExisting || p.SQLiteOptions.BusyTimeoutMS != 0 || p.SQLiteOptions.ForeignKeys || len(p.SQLiteOptions.Attachments) > 0) {
		return p, fmt.Errorf("%w: SQLite options on another engine", ErrInvalid)
	}
	p.ActiveVariant = strings.TrimSpace(p.ActiveVariant)
	if len(p.Variants) > 16 {
		return p, fmt.Errorf("%w: too many connection configurations", ErrInvalid)
	}
	for i, variant := range p.Variants {
		variant.Name = strings.TrimSpace(variant.Name)
		if variant.Name == "" || utf8.RuneCountInString(variant.Name) > 120 ||
			strings.ContainsRune(variant.Name, 0) {
			return p, fmt.Errorf("%w: invalid configuration name", ErrInvalid)
		}
		for j := 0; j < i; j++ {
			if strings.EqualFold(p.Variants[j].Name, variant.Name) {
				return p, fmt.Errorf("%w: duplicate configuration name", ErrInvalid)
			}
		}
		if !p.Kind.IsMySQLFamily() {
			return p, fmt.Errorf("%w: alternative configurations require MySQL", ErrInvalid)
		}
		checked, err := NormalizeProfile(Profile{Name: "configuration", Kind: p.Kind,
			Host: variant.Host, Port: variant.Port, Database: variant.Database,
			Username: variant.Username, TLSMode: variant.TLSMode,
			MySQLAdvanced: variant.MySQLAdvanced})
		if err != nil {
			return p, fmt.Errorf("%w: configuration %s: %v", ErrInvalid, variant.Name, err)
		}
		// The host is a hostname/IP, never a URL or userinfo-bearing DSN.
		if strings.ContainsAny(checked.Host, "@/?!#") {
			return p, fmt.Errorf("%w: invalid configuration host", ErrInvalid)
		}
		variant.Host, variant.Port, variant.Database = checked.Host, checked.Port, checked.Database
		variant.Username, variant.TLSMode = checked.Username, checked.TLSMode
		variant.MySQLAdvanced = checked.MySQLAdvanced
		p.Variants[i] = variant
	}
	if p.ActiveVariant != "" {
		if !p.Kind.IsMySQLFamily() {
			return p, fmt.Errorf("%w: active alternatives require MySQL", ErrInvalid)
		}
		found := false
		for _, variant := range p.Variants {
			if strings.EqualFold(variant.Name, p.ActiveVariant) {
				p.ActiveVariant, found = variant.Name, true
				break
			}
		}
		if !found {
			return p, fmt.Errorf("%w: active configuration does not exist", ErrInvalid)
		}
	}
	p.Host = strings.TrimSpace(p.Host)
	p.Database = strings.TrimSpace(p.Database)
	p.Username = strings.TrimSpace(p.Username)
	p.TLSMode = strings.ToLower(strings.TrimSpace(p.TLSMode))
	// Notes are user-authored multiline profile metadata, not credentials.
	if !utf8.ValidString(p.Notes) || strings.ContainsRune(p.Notes, 0) || len(p.Notes) > 64*1024 {
		return p, fmt.Errorf("%w: connection notes must be valid UTF-8, NUL-free, and at most 64 KiB", ErrInvalid)
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 120 || strings.ContainsRune(p.Name, 0) {
		return p, fmt.Errorf("%w: name must contain 1 to 120 characters", ErrInvalid)
	}
	for _, v := range []string{p.Host, p.Database, p.Username, p.TLSMode} {
		if strings.ContainsRune(v, 0) {
			return p, fmt.Errorf("%w: NUL byte in field", ErrInvalid)
		}
	}
	switch p.Kind {
	case DuckDB:
		var err error
		p.DuckDBOptions, err = NormalizeDuckDBOptions(p.DuckDBOptions)
		if err != nil {
			return p, err
		}
		if p.Database == "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.TLSMode != "" || p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: DuckDB requires a file path or :memory: and no server settings", ErrInvalid)
		}
		if p.Database == ":memory:" && p.DuckDBOptions.ReadOnly {
			return p, fmt.Errorf("%w: an in-memory DuckDB database cannot be read-only", ErrInvalid)
		}
	case MySQL, MariaDB, PostgreSQL, ClickHouse, SQLServer:
		if p.Kind == SQLServer {
			var err error
			p.SQLServerOptions, err = NormalizeSQLServerOptions(p.SQLServerOptions)
			if err != nil {
				return p, err
			}
			if p.Database == "" || p.Username == "" || len([]rune(p.Database)) > 128 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") {
				return p, fmt.Errorf("%w: SQL Server requires a hostname, database and SQL username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "verify-full"
			}
			if p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-full" {
				return p, fmt.Errorf("%w: SQL Server TLS must be disable, require or verify-full", ErrInvalid)
			}
			if p.TLSMode == "disable" && (p.SQLServerOptions.CAFile != "" || p.SQLServerOptions.ServerName != "") {
				return p, fmt.Errorf("%w: SQL Server certificate settings require TLS", ErrInvalid)
			}
		}
		if p.Kind.IsMySQLFamily() {
			var err error
			p.MySQLAdvanced, err = NormalizeMySQLAdvanced(p.MySQLAdvanced)
			if err != nil {
				return p, err
			}
		} else if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on another engine", ErrInvalid)
		}
		if p.Kind == PostgreSQL {
			var err error
			p.PostgreSQLOptions, err = NormalizePostgreSQLOptions(p.PostgreSQLOptions)
			if err != nil {
				return p, err
			}
		}
		if p.Host == "" || p.Port == 0 {
			return p, fmt.Errorf("%w: server host and port are required", ErrInvalid)
		}
		if p.Kind == ClickHouse {
			if strings.ContainsAny(p.Host, "@/?!#\\") || strings.ContainsAny(p.Host, " \t\r\n") || p.Database == "" || p.Username == "" {
				return p, fmt.Errorf("%w: ClickHouse requires a hostname, database and username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "disable"
			}
			if p.TLSMode != "disable" && p.TLSMode != "verify-full" {
				return p, fmt.Errorf("%w: ClickHouse TLS must be disable or verify-full", ErrInvalid)
			}
		}
		switch p.TLSMode {
		case "", "disable", "prefer", "require", "verify-ca", "verify-full":
		default:
			return p, fmt.Errorf("%w: unsupported TLS mode", ErrInvalid)
		}
	case SQLite:
		var sqliteErr error
		p.SQLiteOptions, sqliteErr = NormalizeSQLiteOptions(p.SQLiteOptions)
		if sqliteErr != nil {
			return p, sqliteErr
		}
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on SQLite profile", ErrInvalid)
		}
		if p.Database == "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.TLSMode != "" {
			return p, fmt.Errorf("%w: SQLite requires a file path and no server settings", ErrInvalid)
		}
	default:
		return p, fmt.Errorf("%w: unsupported database kind", ErrInvalid)
	}
	return p, nil
}
