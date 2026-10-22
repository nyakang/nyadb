// Package connections manages non-secret connection profiles. It has no UI
// dependency and deliberately has no password or raw DSN field.
package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind string

const (
	MySQL      Kind = "mysql"
	PostgreSQL Kind = "postgresql"
	SQLite     Kind = "sqlite"
)

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
	ID            int64
	Name          string
	GroupName     string // Optional virtual navigation group; never a database name.
	Starred       bool   // Local navigation favorite; never sent to a database.
	Kind          Kind
	Host          string
	Port          uint16
	Database      string
	Username      string
	TLSMode       string
	MySQLAdvanced MySQLAdvanced
	Notes         string
	Variants      []ConnectionVariant
	ActiveVariant string // Empty selects the primary configuration.
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func normalize(p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.GroupName = strings.TrimSpace(p.GroupName)
	if p.GroupName != "" {
		if err := validateGroupName(p.GroupName); err != nil {
			return p, err
		}
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
		if p.Kind != MySQL {
			return p, fmt.Errorf("%w: alternative configurations require MySQL", ErrInvalid)
		}
		checked, err := normalize(Profile{Name: "configuration", Kind: MySQL,
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
		if p.Kind != MySQL {
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
	case MySQL, PostgreSQL:
		if p.Kind == MySQL {
			var err error
			p.MySQLAdvanced, err = normalizeMySQLAdvanced(p.MySQLAdvanced)
			if err != nil {
				return p, err
			}
		} else if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on PostgreSQL profile", ErrInvalid)
		}
		if p.Host == "" || p.Port == 0 {
			return p, fmt.Errorf("%w: server host and port are required", ErrInvalid)
		}
		switch p.TLSMode {
		case "", "disable", "prefer", "require", "verify-ca", "verify-full":
		default:
			return p, fmt.Errorf("%w: unsupported TLS mode", ErrInvalid)
		}
	case SQLite:
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

const profileColumns = "id, name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, notes, variants_json, active_variant, created_at_ms, updated_at_ms, group_name, starred"

func scanProfile(scan func(...any) error) (Profile, error) {
	var p Profile
	var advancedJSON, variantsJSON string
	var port int64
	var created, updated int64
	var starred int64
	err := scan(&p.ID, &p.Name, &p.Kind, &p.Host, &port, &p.Database,
		&p.Username, &p.TLSMode, &advancedJSON, &p.Notes, &variantsJSON, &p.ActiveVariant, &created, &updated, &p.GroupName, &starred)
	p.Starred = starred != 0
	if err != nil {
		return Profile{}, err
	}
	if port < 0 || port > 65535 {
		return Profile{}, fmt.Errorf("corrupt profile port: %d", port)
	}
	p.Port = uint16(port)
	if p.Kind == MySQL {
		if advancedJSON == "" {
			p.MySQLAdvanced = DefaultMySQLAdvanced()
		} else if err := json.Unmarshal([]byte(advancedJSON), &p.MySQLAdvanced); err != nil {
			return Profile{}, fmt.Errorf("decode MySQL advanced settings: %w", err)
		}
		var err error
		if p.MySQLAdvanced, err = normalizeMySQLAdvanced(p.MySQLAdvanced); err != nil {
			return Profile{}, fmt.Errorf("invalid saved MySQL advanced settings: %w", err)
		}
	} else if advancedJSON != "" {
		return Profile{}, fmt.Errorf("unexpected MySQL advanced settings on %s", p.Kind)
	}
	if variantsJSON != "" {
		if err := json.Unmarshal([]byte(variantsJSON), &p.Variants); err != nil {
			return Profile{}, fmt.Errorf("decode connection configurations: %w", err)
		}
		if p.Kind != MySQL && len(p.Variants) > 0 {
			return Profile{}, fmt.Errorf("unexpected variants on %s", p.Kind)
		}
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()
	return p, nil
}

func (s *Store) Create(ctx context.Context, p Profile) (Profile, error) {
	p, err := normalize(p)
	if err != nil {
		return Profile{}, err
	}
	if err := s.requireGroup(ctx, &p); err != nil {
		return Profile{}, err
	}
	advancedJSON := ""
	if p.Kind == MySQL {
		encoded, err := json.Marshal(p.MySQLAdvanced)
		if err != nil {
			return Profile{}, fmt.Errorf("encode MySQL advanced settings: %w", err)
		}
		advancedJSON = string(encoded)
	}
	variantsJSON, err := json.Marshal(p.Variants)
	if err != nil {
		return Profile{}, fmt.Errorf("encode configurations: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO connection_profiles (name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, notes, variants_json, active_variant, created_at_ms, updated_at_ms, group_name, starred) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, p.Notes, string(variantsJSON), p.ActiveVariant, now.UnixMilli(), now.UnixMilli(), p.GroupName, p.Starred)
	if err != nil {
		return Profile{}, fmt.Errorf("create connection profile: %w", err)
	}
	p.ID, err = result.LastInsertId()
	if err != nil {
		return Profile{}, fmt.Errorf("read new connection profile ID: %w", err)
	}
	p.CreatedAt, p.UpdatedAt = now, now
	return p, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Profile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx,
		"SELECT "+profileColumns+" FROM connection_profiles WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("read connection profile: %w", err)
	}
	return p, nil
}

func (s *Store) List(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+profileColumns+" FROM connection_profiles ORDER BY name COLLATE NOCASE, id")
	if err != nil {
		return nil, fmt.Errorf("list connection profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		p, err := scanProfile(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan connection profile: %w", err)
		}
		profiles = append(profiles, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection profiles: %w", err)
	}
	return profiles, nil
}

func (s *Store) Update(ctx context.Context, p Profile) (Profile, error) {
	if p.ID <= 0 {
		return Profile{}, ErrNotFound
	}
	p, err := normalize(p)
	if err != nil {
		return Profile{}, err
	}
	if err := s.requireGroup(ctx, &p); err != nil {
		return Profile{}, err
	}
	advancedJSON := ""
	if p.Kind == MySQL {
		encoded, err := json.Marshal(p.MySQLAdvanced)
		if err != nil {
			return Profile{}, fmt.Errorf("encode MySQL advanced settings: %w", err)
		}
		advancedJSON = string(encoded)
	}
	variantsJSON, err := json.Marshal(p.Variants)
	if err != nil {
		return Profile{}, fmt.Errorf("encode configurations: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"UPDATE connection_profiles SET name=?, kind=?, host=?, port=?, database_name=?, username=?, tls_mode=?, mysql_advanced_json=?, notes=?, variants_json=?, active_variant=?, updated_at_ms=?, group_name=?, starred=? WHERE id=?",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, p.Notes, string(variantsJSON), p.ActiveVariant, now.UnixMilli(), p.GroupName, p.Starred, p.ID)
	if err != nil {
		return Profile{}, fmt.Errorf("update connection profile: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Profile{}, fmt.Errorf("check profile update: %w", err)
	}
	if count == 0 {
		return Profile{}, ErrNotFound
	}
	return s.Get(ctx, p.ID)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM connection_profiles WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete connection profile: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check profile deletion: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
