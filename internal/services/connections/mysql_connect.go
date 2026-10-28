package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

var ErrMySQLUnsupported = errors.New("MySQL connection setting not supported yet")

// DirectMySQLConfig builds a MySQL TCP configuration with optional verified TLS without ever
// formatting a credential-bearing DSN or storing credentials in Profile.
// Plain TCP is permitted when TLS is not configured; selected TLS settings
// retain strict certificate and hostname verification. Unsupported tunneling
// settings fail closed before any network I/O.
func DirectMySQLConfig(s ActiveSettings, password string) (*mysql.Config, error) {
	if strings.TrimSpace(s.Host) == "" || s.Port == 0 ||
		strings.ContainsAny(s.Host, "@/?!#\r\n\x00") {
		return nil, fmt.Errorf("%w: invalid MySQL host or port", ErrInvalid)
	}
	a, err := normalizeMySQLAdvanced(s.MySQLAdvanced)
	if err != nil {
		return nil, err
	}
	switch {
	case s.TLSMode == "prefer" || s.TLSMode == "verify-ca":
		return nil, fmt.Errorf("%w: TLS mode without hostname verification", ErrMySQLUnsupported)
	case a.SSH.Enabled:
		return nil, fmt.Errorf("%w: SSH tunnel", ErrMySQLUnsupported)
	case a.HTTP.Enabled:
		return nil, fmt.Errorf("%w: HTTP tunnel", ErrMySQLUnsupported)
	case a.NamedPipe:
		return nil, fmt.Errorf("%w: named pipe", ErrMySQLUnsupported)
	case a.ClientDriver != "默认":
		return nil, fmt.Errorf("%w: legacy client driver", ErrMySQLUnsupported)
	case a.Charset != "自动" && a.Charset != "None":
		return nil, fmt.Errorf("%w: custom character set", ErrMySQLUnsupported)
	case a.Compression || a.KeepAlive || a.AutoConnect:
		return nil, fmt.Errorf("%w: advanced connection behavior", ErrMySQLUnsupported)
	case a.Compatibility.Enabled || a.Compatibility.ForceSQLMode ||
		a.Compatibility.ForceLowerCase || a.Compatibility.ForceNDB ||
		a.Compatibility.ForceDatabaseMethod || a.Compatibility.ForceViewMethod:
		return nil, fmt.Errorf("%w: compatibility overrides", ErrMySQLUnsupported)
	case a.SettingsLocation != "":
		return nil, fmt.Errorf("%w: custom settings location", ErrMySQLUnsupported)
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(strings.Trim(s.Host, "[]"), fmt.Sprint(s.Port))
	cfg.User, cfg.Passwd = s.Username, password
	cfg.DBName = s.Database
	if a.SSL.Enabled || (s.TLSMode != "" && s.TLSMode != "disable") {
		tlsConfig, err := verifiedMySQLTLS(s.Host, a.SSL)
		if err != nil {
			return nil, err
		}
		cfg.TLS = tlsConfig
	}
	// Never allow cleartext or insecure legacy authentication plugins.
	cfg.AllowCleartextPasswords = false
	cfg.AllowOldPasswords = false
	cfg.AllowFallbackToPlaintext = false
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	// Even if "connect timeout" is unchecked, bound a one-shot test instead
	// of accepting the OS's potentially unbounded TCP timeout.
	cfg.Timeout = 10 * time.Second
	if a.ConnectTimeout {
		cfg.Timeout = time.Duration(a.ConnectTimeoutSeconds) * time.Second
	}
	if a.ReadTimeout {
		cfg.ReadTimeout = time.Duration(a.ReadTimeoutSeconds) * time.Second
	}
	if a.WriteTimeout {
		cfg.WriteTimeout = time.Duration(a.WriteTimeoutSeconds) * time.Second
	}
	return cfg, nil
}

// OpenDirectMySQL establishes a real, authenticated MySQL connection.
// The caller owns the returned *sql.DB and MUST Close it. No SQL statements
// are executed: PingContext performs the transport and authentication check.
// Its context controls the test deadline and cancels the handshake.
func OpenDirectMySQL(ctx context.Context, s ActiveSettings, password string) (*sql.DB, error) {
	cfg, err := DirectMySQLConfig(s, password)
	if err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("create MySQL connector: %w", err)
	}
	db := sql.OpenDB(connector)
	// Metadata and data browsing may overlap. SQL editor sessions use their
	// own pool through OpenDirectMySQLPools and cannot starve this pool.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(3 * time.Minute)
	testCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err = db.PingContext(testCtx); err != nil {
		_ = db.Close()
		if testCtx.Err() != nil {
			return nil, testCtx.Err()
		}
		return nil, err
	}
	return db, nil
}

// NyaDB's SQL-tab lease budget is intentionally separate from the metadata
// pool. Navicat's Conn.SessionLimit scope has not yet been runtime-verified.
// Even if the optional setting exceeds the built-in safety cap, we never
// silently create unbounded stateful database sessions.
const maxSQLTabSessions = 8

func mysqlSQLTabLimit(a MySQLAdvanced) int {
	if !a.LimitSessions {
		return maxSQLTabSessions
	}
	if a.MaxSessions < 1 {
		return 1 // invalid persisted settings must not disable protection
	}
	return min(a.MaxSessions, maxSQLTabSessions)
}

// OpenDirectMySQLPools isolates short-lived metadata/data operations from
// long-lived interactive SQL tabs. Each tab reserves one connection from the
// SQL pool until the tab is closed; idle SQL connections are never recycled
// into another tab with session variables or an open transaction.
func OpenDirectMySQLPools(ctx context.Context, s ActiveSettings, password string) (*sql.DB, *sql.DB, error) {
	metadata, err := OpenDirectMySQL(ctx, s, password)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := DirectMySQLConfig(s, password)
	if err != nil {
		_ = metadata.Close()
		return nil, nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		_ = metadata.Close()
		return nil, nil, err
	}
	sqlPool := sql.OpenDB(connector)
	sqlPool.SetMaxOpenConns(mysqlSQLTabLimit(s.MySQLAdvanced))
	sqlPool.SetMaxIdleConns(0)
	// A transaction/temporary table must not be evicted by a wall-clock TTL.
	// Disconnection and tab disposal explicitly own session cleanup.
	return metadata, sqlPool, nil
}

// TestDirectMySQLConnection opens, authenticates and closes a single session.
// Only call after an explicit user action, never when browsing or saving.
func TestDirectMySQLConnection(ctx context.Context, s ActiveSettings, password string) error {
	db, err := OpenDirectMySQL(ctx, s, password)
	if err != nil {
		return err
	}
	return db.Close()
}
