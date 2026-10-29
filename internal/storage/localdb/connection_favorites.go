package localdb

import (
	"context"
	"fmt"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *ProfileStore) SetStarred(ctx context.Context, profileID int64, starred bool) error {
	if profileID <= 0 {
		return connections.ErrInvalid
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
		return connections.ErrNotFound
	}
	return nil
}
