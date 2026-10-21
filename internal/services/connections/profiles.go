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
type Profile struct {
	ID            int64
	Name          string
	Kind          Kind
	Host          string
	Port          uint16
	Database      string
	Username      string
	TLSMode       string
	MySQLAdvanced MySQLAdvanced
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
	p.Host = strings.TrimSpace(p.Host)
	p.Database = strings.TrimSpace(p.Database)
	p.Username = strings.TrimSpace(p.Username)
	p.TLSMode = strings.ToLower(strings.TrimSpace(p.TLSMode))
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

const profileColumns = "id, name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, created_at_ms, updated_at_ms"

func scanProfile(scan func(...any) error) (Profile, error) {
	var p Profile
	var advancedJSON string
	var port int64
	var created, updated int64
	err := scan(&p.ID, &p.Name, &p.Kind, &p.Host, &port, &p.Database,
		&p.Username, &p.TLSMode, &advancedJSON, &created, &updated)
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
	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()
	return p, nil
}

func (s *Store) Create(ctx context.Context, p Profile) (Profile, error) {
	p, err := normalize(p)
	if err != nil {
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
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO connection_profiles (name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, now.UnixMilli(), now.UnixMilli())
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
	advancedJSON := ""
	if p.Kind == MySQL {
		encoded, err := json.Marshal(p.MySQLAdvanced)
		if err != nil {
			return Profile{}, fmt.Errorf("encode MySQL advanced settings: %w", err)
		}
		advancedJSON = string(encoded)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"UPDATE connection_profiles SET name=?, kind=?, host=?, port=?, database_name=?, username=?, tls_mode=?, mysql_advanced_json=?, updated_at_ms=? WHERE id=?",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, now.UnixMilli(), p.ID)
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
