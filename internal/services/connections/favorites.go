package connections

import (
	"context"
	"fmt"
)

type starredRepository interface {
	SetStarred(context.Context, int64, bool) error
}

// SetStarred changes navigation metadata independently of connection
// credentials, open sessions, or the connection editor's current draft.
func (s *Service) SetStarred(ctx context.Context, profileID int64, starred bool) error {
	if repo, ok := s.repository.(starredRepository); ok {
		return repo.SetStarred(ctx, profileID, starred)
	}
	return fmt.Errorf("connection favorites storage unavailable")
}

func (s *Store) SetStarred(ctx context.Context, profileID int64, starred bool) error {
	if profileID <= 0 {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, "UPDATE connection_profiles SET starred=? WHERE id=?", starred, profileID)
	if err != nil {
		return fmt.Errorf("set connection favorite: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
