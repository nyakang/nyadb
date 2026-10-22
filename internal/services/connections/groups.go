package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Group is a virtual connection folder, not a schema or a network destination.
// Empty folders are persisted independently of their connection profiles.
type Group struct {
	Name string
}

func validateGroupName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: invalid connection group name", ErrInvalid)
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return fmt.Errorf("%w: invalid connection group character", ErrInvalid)
		}
	}
	return nil
}

type groupRepository interface {
	ListGroups(context.Context) ([]Group, error)
	CreateGroup(context.Context, string) error
	RenameGroup(context.Context, string, string) error
	DeleteGroup(context.Context, string) error
	MoveToGroup(context.Context, int64, string) error
}

func (s *Service) groups() groupRepository {
	groups, _ := s.repository.(groupRepository)
	return groups
}

func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	if repo := s.groups(); repo != nil {
		return repo.ListGroups(ctx)
	}
	return nil, errors.New("connection group storage unavailable")
}
func (s *Service) CreateGroup(ctx context.Context, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.CreateGroup(ctx, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) RenameGroup(ctx context.Context, old, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.RenameGroup(ctx, old, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.DeleteGroup(ctx, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) MoveToGroup(ctx context.Context, profileID int64, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.MoveToGroup(ctx, profileID, name)
	}
	return errors.New("connection group storage unavailable")
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM connection_groups ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var group Group
		if err := rows.Scan(&group.Name); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) CreateGroup(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := validateGroupName(name); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO connection_groups(name) VALUES (?)", name); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *Store) RenameGroup(ctx context.Context, old, name string) error {
	name = strings.TrimSpace(name)
	if err := validateGroupName(old); err != nil {
		return err
	}
	if err := validateGroupName(name); err != nil {
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
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE connection_profiles SET group_name=? WHERE group_name=? COLLATE NOCASE", name, old); err != nil {
		return fmt.Errorf("rename group assignments: %w", err)
	}
	return tx.Commit()
}

// Deleting a virtual folder does not delete its profiles or active sessions.
func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	if err := validateGroupName(name); err != nil {
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
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE connection_profiles SET group_name='' WHERE group_name=? COLLATE NOCASE", name); err != nil {
		return err
	}
	return tx.Commit()
}

// A group assignment changes navigation only; session state is untouched.
func (s *Store) MoveToGroup(ctx context.Context, profileID int64, name string) error {
	if name != "" {
		if err := validateGroupName(name); err != nil {
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
				return ErrNotFound
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
		return ErrNotFound
	}
	return tx.Commit()
}
