package localdb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nyakang/nyadb/internal/services/favorites"
)

type FavoritesRepository struct{ db *sql.DB }

func NewFavoritesRepository(db *sql.DB) *FavoritesRepository { return &FavoritesRepository{db: db} }
func (r *FavoritesRepository) Read(ctx context.Context) (string, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM app_preferences WHERE key = ?", favorites.PreferenceKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}
func (r *FavoritesRepository) Publish(ctx context.Context, before, after string) (bool, error) {
	// A stale nonempty token cannot insert a record after external deletion.
	if before != "" {
		result, err := r.db.ExecContext(ctx, "UPDATE app_preferences SET value = ? WHERE key = ? AND value = ?", after, favorites.PreferenceKey, before)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		return n == 1, err
	}
	result, err := r.db.ExecContext(ctx, "INSERT INTO app_preferences (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE app_preferences.value = ''", favorites.PreferenceKey, after)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
