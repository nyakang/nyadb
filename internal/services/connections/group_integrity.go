package connections

import (
	"context"
	"database/sql"
	"errors"
)

// requireGroup prevents stale editor drafts from resurrecting a removed
// virtual folder. Resolve the stored casing before saving the profile.
func (s *Store) requireGroup(ctx context.Context, profile *Profile) error {
	if profile.GroupName == "" {
		return nil
	}
	var canonical string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM connection_groups WHERE name=? COLLATE NOCASE", profile.GroupName).Scan(&canonical); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	profile.GroupName = canonical
	return nil
}
