package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// ProfileStore persists non-secret connection settings in the local SQLite database.
// The caller owns the database and its lifetime.
type ProfileStore struct{ db *sql.DB }

// NewProfileStore wraps an initialized local configuration database.
func NewProfileStore(db *sql.DB) *ProfileStore { return &ProfileStore{db: db} }

var _ connections.Repository = (*ProfileStore)(nil)

const profileColumns = "id, name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, notes, variants_json, active_variant, created_at_ms, updated_at_ms, group_name, starred"

func scanProfile(scan func(...any) error) (connections.Profile, error) {
	var p connections.Profile
	var advancedJSON, variantsJSON string
	var port int64
	var created, updated int64
	var starred int64
	err := scan(&p.ID, &p.Name, &p.Kind, &p.Host, &port, &p.Database,
		&p.Username, &p.TLSMode, &advancedJSON, &p.Notes, &variantsJSON, &p.ActiveVariant, &created, &updated, &p.GroupName, &starred)
	p.Starred = starred != 0
	if err != nil {
		return connections.Profile{}, err
	}
	if port < 0 || port > 65535 {
		return connections.Profile{}, fmt.Errorf("corrupt profile port: %d", port)
	}
	p.Port = uint16(port)
	if p.Kind == connections.MySQL {
		if advancedJSON == "" {
			p.MySQLAdvanced = connections.DefaultMySQLAdvanced()
		} else if err := json.Unmarshal([]byte(advancedJSON), &p.MySQLAdvanced); err != nil {
			return connections.Profile{}, fmt.Errorf("decode MySQL advanced settings: %w", err)
		}
		var err error
		if p.MySQLAdvanced, err = connections.NormalizeMySQLAdvanced(p.MySQLAdvanced); err != nil {
			return connections.Profile{}, fmt.Errorf("invalid saved MySQL advanced settings: %w", err)
		}
	} else if advancedJSON != "" {
		return connections.Profile{}, fmt.Errorf("unexpected MySQL advanced settings on %s", p.Kind)
	}
	if variantsJSON != "" {
		if err := json.Unmarshal([]byte(variantsJSON), &p.Variants); err != nil {
			return connections.Profile{}, fmt.Errorf("decode connection configurations: %w", err)
		}
		if p.Kind != connections.MySQL && len(p.Variants) > 0 {
			return connections.Profile{}, fmt.Errorf("unexpected variants on %s", p.Kind)
		}
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()
	return p, nil
}

func (s *ProfileStore) Create(ctx context.Context, p connections.Profile) (connections.Profile, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return connections.Profile{}, err
	}
	if err := s.requireGroup(ctx, &p); err != nil {
		return connections.Profile{}, err
	}
	advancedJSON := ""
	if p.Kind == connections.MySQL {
		encoded, err := json.Marshal(p.MySQLAdvanced)
		if err != nil {
			return connections.Profile{}, fmt.Errorf("encode MySQL advanced settings: %w", err)
		}
		advancedJSON = string(encoded)
	}
	variantsJSON, err := json.Marshal(p.Variants)
	if err != nil {
		return connections.Profile{}, fmt.Errorf("encode configurations: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO connection_profiles (name, kind, host, port, database_name, username, tls_mode, mysql_advanced_json, notes, variants_json, active_variant, created_at_ms, updated_at_ms, group_name, starred) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, p.Notes, string(variantsJSON), p.ActiveVariant, now.UnixMilli(), now.UnixMilli(), p.GroupName, p.Starred)
	if err != nil {
		return connections.Profile{}, fmt.Errorf("create connection profile: %w", err)
	}
	p.ID, err = result.LastInsertId()
	if err != nil {
		return connections.Profile{}, fmt.Errorf("read new connection profile ID: %w", err)
	}
	p.CreatedAt, p.UpdatedAt = now, now
	return p, nil
}

func (s *ProfileStore) Get(ctx context.Context, id int64) (connections.Profile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx,
		"SELECT "+profileColumns+" FROM connection_profiles WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return connections.Profile{}, connections.ErrNotFound
	}
	if err != nil {
		return connections.Profile{}, fmt.Errorf("read connection profile: %w", err)
	}
	return p, nil
}

func (s *ProfileStore) List(ctx context.Context) ([]connections.Profile, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+profileColumns+" FROM connection_profiles ORDER BY name COLLATE NOCASE, id")
	if err != nil {
		return nil, fmt.Errorf("list connection profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]connections.Profile, 0)
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

func (s *ProfileStore) Update(ctx context.Context, p connections.Profile) (connections.Profile, error) {
	if p.ID <= 0 {
		return connections.Profile{}, connections.ErrNotFound
	}
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return connections.Profile{}, err
	}
	if err := s.requireGroup(ctx, &p); err != nil {
		return connections.Profile{}, err
	}
	advancedJSON := ""
	if p.Kind == connections.MySQL {
		encoded, err := json.Marshal(p.MySQLAdvanced)
		if err != nil {
			return connections.Profile{}, fmt.Errorf("encode MySQL advanced settings: %w", err)
		}
		advancedJSON = string(encoded)
	}
	variantsJSON, err := json.Marshal(p.Variants)
	if err != nil {
		return connections.Profile{}, fmt.Errorf("encode configurations: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		"UPDATE connection_profiles SET name=?, kind=?, host=?, port=?, database_name=?, username=?, tls_mode=?, mysql_advanced_json=?, notes=?, variants_json=?, active_variant=?, updated_at_ms=?, group_name=?, starred=? WHERE id=?",
		p.Name, p.Kind, p.Host, p.Port, p.Database, p.Username, p.TLSMode, advancedJSON, p.Notes, string(variantsJSON), p.ActiveVariant, now.UnixMilli(), p.GroupName, p.Starred, p.ID)
	if err != nil {
		return connections.Profile{}, fmt.Errorf("update connection profile: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return connections.Profile{}, fmt.Errorf("check profile update: %w", err)
	}
	if count == 0 {
		return connections.Profile{}, connections.ErrNotFound
	}
	return s.Get(ctx, p.ID)
}

func (s *ProfileStore) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM connection_profiles WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete connection profile: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check profile deletion: %w", err)
	}
	if count == 0 {
		return connections.ErrNotFound
	}
	return nil
}
