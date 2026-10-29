package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *ProfileStore) ListGroups(ctx context.Context) ([]connections.Group, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM connection_groups ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	groups := []connections.Group{}
	for rows.Next() {
		var group connections.Group
		if err := rows.Scan(&group.Name); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *ProfileStore) CreateGroup(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := connections.ValidateGroupName(name); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO connection_groups(name) VALUES (?)", name); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *ProfileStore) RenameGroup(ctx context.Context, old, name string) error {
	name = strings.TrimSpace(name)
	if err := connections.ValidateGroupName(old); err != nil {
		return err
	}
	if err := connections.ValidateGroupName(name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE connection_groups SET name=? WHERE name=? COLLATE NOCASE", name, old)
	if err != nil {
		return fmt.Errorf("rename group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return connections.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE connection_profiles SET group_name=? WHERE group_name=? COLLATE NOCASE", name, old); err != nil {
		return fmt.Errorf("rename group assignments: %w", err)
	}
	return tx.Commit()
}

// Deleting a virtual folder does not delete its profiles or active sessions.
func (s *ProfileStore) DeleteGroup(ctx context.Context, name string) error {
	if err := connections.ValidateGroupName(name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM connection_groups WHERE name=? COLLATE NOCASE", name)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return connections.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE connection_profiles SET group_name='' WHERE group_name=? COLLATE NOCASE", name); err != nil {
		return err
	}
	return tx.Commit()
}

// A group assignment changes navigation only; session state is untouched.
func (s *ProfileStore) MoveToGroup(ctx context.Context, profileID int64, name string) error {
	if name != "" {
		if err := connections.ValidateGroupName(name); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if name != "" {
		var canonical string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM connection_groups WHERE name=? COLLATE NOCASE", name).Scan(&canonical); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return connections.ErrNotFound
			}
			return err
		}
		name = canonical
	}
	res, err := tx.ExecContext(ctx, "UPDATE connection_profiles SET group_name=? WHERE id=?", name, profileID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return connections.ErrNotFound
	}
	return tx.Commit()
}

// requireGroup prevents stale editor drafts from resurrecting a removed
// virtual folder. Resolve the stored casing before saving the profile.
func (s *ProfileStore) requireGroup(ctx context.Context, profile *connections.Profile) error {
	if profile.GroupName == "" {
		return nil
	}
	var canonical string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM connection_groups WHERE name=? COLLATE NOCASE", profile.GroupName).Scan(&canonical); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return connections.ErrNotFound
		}
		return err
	}
	profile.GroupName = canonical
	return nil
}
