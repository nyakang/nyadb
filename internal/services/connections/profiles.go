// Package connections manages non-secret connection profiles and configuration
// services independently of UI, persistence, and database drivers.
package connections

import (
	"errors"
	"time"
)

type Kind string

const (
	MySQL      Kind = "mysql"
	MariaDB    Kind = "mariadb"
	PostgreSQL Kind = "postgresql"
	SQLite     Kind = "sqlite"
	ClickHouse Kind = "clickhouse"
)

// IsMySQLFamily identifies products sharing the MySQL client and connection
// settings. Product identity and capabilities remain separate.
func (k Kind) IsMySQLFamily() bool { return k == MySQL || k == MariaDB }

var (
	ErrNotFound = errors.New("connection profile not found")
	ErrInvalid  = errors.New("invalid connection profile")
)

// Profile stores only public connection settings. Credentials are provided
// separately at connect time, never persisted in this record.
// ConnectionVariant is a named alternative settings set under one MySQL
// connection, not another top-level connection. Only non-secret fields persist.
type ConnectionVariant struct {
	Name          string
	Host          string
	Port          uint16
	Database      string
	Username      string
	TLSMode       string
	MySQLAdvanced MySQLAdvanced
}

type Profile struct {
	PostgreSQLOptions PostgreSQLOptions
	SQLiteOptions     SQLiteOptions
	ID                int64
	Name              string
	GroupName         string // Optional virtual navigation group; never a database name.
	Color             string // Local fixed-palette accent; never sent to a database.
	Starred           bool   // Local navigation favorite; never sent to a database.
	Kind              Kind
	Host              string
	Port              uint16
	Database          string
	Username          string
	TLSMode           string
	MySQLAdvanced     MySQLAdvanced
	Notes             string
	Variants          []ConnectionVariant
	ActiveVariant     string // Empty selects the primary configuration.
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
