package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/transport"
)

var ErrMySQLUnsupported = errors.New("MySQL connection setting not supported yet")

// DirectMySQLConfig builds a MySQL TCP configuration with optional verified TLS without ever
// formatting a credential-bearing DSN or storing credentials in Profile.
// Plain TCP is permitted when TLS is not configured; selected TLS settings
// retain strict certificate and hostname verification. Unsupported tunneling
// settings fail closed before any network I/O.
func DirectMySQLConfig(s connections.ActiveSettings, password string) (*mysqldriver.Config, error) {
	return mysqlConfig(s, password, false)
}

func ValidateMySQLTransportSettings(s connections.ActiveSettings, password string) error {
	_, err := mysqlConfig(s, password, true)
	return err
}

func mysqlConfig(s connections.ActiveSettings, password string, allowTunnel bool) (*mysqldriver.Config, error) {
	if strings.TrimSpace(s.Host) == "" || s.Port == 0 ||
		strings.ContainsAny(s.Host, "@/?!#\r\n\x00") {
		return nil, fmt.Errorf("%w: invalid MySQL host or port", connections.ErrInvalid)
	}
	a, err := connections.NormalizeMySQLAdvanced(s.MySQLAdvanced)
	if err != nil {
		return nil, err
	}
	switch {
	case (a.SSH.Enabled || a.HTTP.Enabled) && !allowTunnel:
		return nil, fmt.Errorf("%w: tunnel provider required", ErrMySQLUnsupported)
	case s.TLSMode == "prefer" || s.TLSMode == "verify-ca":
		return nil, fmt.Errorf("%w: TLS mode without hostname verification", ErrMySQLUnsupported)
	case a.NamedPipe:
		return nil, fmt.Errorf("%w: named pipe", ErrMySQLUnsupported)
	case a.ClientDriver != "默认":
		return nil, fmt.Errorf("%w: legacy client driver", ErrMySQLUnsupported)
	case a.Compatibility.Enabled || a.Compatibility.ForceSQLMode ||
		a.Compatibility.ForceLowerCase || a.Compatibility.ForceNDB ||
		a.Compatibility.ForceDatabaseMethod || a.Compatibility.ForceViewMethod:
		return nil, fmt.Errorf("%w: compatibility overrides", ErrMySQLUnsupported)
	case a.SettingsLocation != "":
		return nil, fmt.Errorf("%w: custom settings location", ErrMySQLUnsupported)
	}
	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(strings.Trim(s.Host, "[]"), fmt.Sprint(s.Port))
	cfg.User, cfg.Passwd = s.Username, password
	cfg.DBName = s.Database
	if a.Charset != "自动" && a.Charset != "None" {
		if err := cfg.Apply(mysqldriver.Charset(a.Charset, "")); err != nil {
			return nil, err
		}
	}
	if err := cfg.Apply(mysqldriver.EnableCompression(a.Compression)); err != nil {
		return nil, err
	}
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
// The caller owns the returned *sql.DB and MUST Close it. No user SQL or profile initialization SQL is executed; the driver may apply
// explicitly configured character-set options before the PingContext check.
// Its context controls the test deadline and cancels the handshake.
func OpenDirectMySQL(ctx context.Context, s connections.ActiveSettings, password string) (*sql.DB, error) {
	cfg, err := DirectMySQLConfig(s, password)
	if err != nil {
		return nil, err
	}
	if s.MySQLAdvanced.SSH.Enabled || s.MySQLAdvanced.HTTP.Enabled {
		return nil, fmt.Errorf("%w: tunnel credentials and host confirmation provider required", ErrMySQLUnsupported)
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("create MySQL connector: %w", err)
	}
	db := sql.OpenDB(connector)
	// Metadata and data browsing may overlap. SQL editor sessions use their
	// own pool through OpenDirectMySQLPools and cannot starve this pool.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	// A fixed TTL would evict metadata sessions before the default
	// 240-second heartbeat, even if they are still healthy.
	if !s.MySQLAdvanced.KeepAlive {
		db.SetConnMaxLifetime(3 * time.Minute)
	}
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

func mysqlSQLTabLimit(a connections.MySQLAdvanced) int {
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
func OpenDirectMySQLPools(ctx context.Context, s connections.ActiveSettings, password string) (*sql.DB, *sql.DB, error) {
	if s.MySQLAdvanced.SSH.Enabled || s.MySQLAdvanced.HTTP.Enabled {
		return nil, nil, fmt.Errorf("%w: tunnel provider required", ErrMySQLUnsupported)
	}
	db, sqlDB, _, err := openMySQLPools(ctx, s, password, nil, "")
	return db, sqlDB, err
}

// openMySQLPools injects one scope into both pools, so every SQL lease and data
// transaction uses the same tunnel lifetime. Pools never reconstruct a revoked
// scope, and only an explicit session Connect creates a replacement.
func openMySQLPools(ctx context.Context, s connections.ActiveSettings, password string, dialer transport.Dialer, initialSQL string) (*sql.DB, *sql.DB, transport.Dialer, error) {
	if dialer == nil && (s.MySQLAdvanced.SSH.Enabled || s.MySQLAdvanced.HTTP.Enabled) {
		return nil, nil, nil, fmt.Errorf("%w: tunnel provider required", ErrMySQLUnsupported)
	}
	cfg, err := mysqlConfig(s, password, true)
	if err != nil {
		if dialer != nil {
			_ = dialer.Close()
		}
		return nil, nil, nil, err
	}
	if dialer != nil {
		cfg.DialFunc = dialer.DialContext
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		if dialer != nil {
			_ = dialer.Close()
		}
		return nil, nil, nil, err
	}
	var initialized driver.Connector = connector
	if initialSQL != "" {
		initialized = WrapMySQLInitialSQL(connector, initialSQL)
	}
	metadata := sql.OpenDB(initialized)
	metadata.SetMaxOpenConns(4)
	metadata.SetMaxIdleConns(2)
	if !s.MySQLAdvanced.KeepAlive {
		metadata.SetConnMaxLifetime(3 * time.Minute)
	}
	testCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err = metadata.PingContext(testCtx); err != nil {
		_ = metadata.Close()
		if dialer != nil {
			_ = dialer.Close()
		}
		if testCtx.Err() != nil {
			err = testCtx.Err()
		}
		return nil, nil, nil, err
	}
	sqlPool := sql.OpenDB(initialized)
	sqlPool.SetMaxOpenConns(mysqlSQLTabLimit(s.MySQLAdvanced))
	sqlPool.SetMaxIdleConns(0)
	return metadata, sqlPool, dialer, nil
}

// TestDirectMySQLConnection opens, authenticates and closes a single session.
// Only call after an explicit user action, never when browsing or saving.
func TestDirectMySQLConnection(ctx context.Context, s connections.ActiveSettings, password string) error {
	db, err := OpenDirectMySQL(ctx, s, password)
	if err != nil {
		return err
	}
	return db.Close()
}

// TestMySQLTransportConnection authenticates one session over the caller-owned
// scope and closes it without executing profile initialization SQL.
func TestMySQLTransportConnection(ctx context.Context, s connections.ActiveSettings, password string, dialer transport.Dialer) error {
	db, sqlDB, scope, err := openMySQLPools(ctx, s, password, dialer, "")
	if err != nil {
		return err
	}
	if scope != nil {
		defer scope.Close()
	}
	return errors.Join(sqlDB.Close(), db.Close())
}
