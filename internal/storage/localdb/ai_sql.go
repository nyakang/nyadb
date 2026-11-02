package localdb

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/services/aisql"
)

type AISQLRepository struct{ db *sql.DB }

func NewAISQLRepository(db *sql.DB) *AISQLRepository { return &AISQLRepository{db: db} }
func (r *AISQLRepository) Load(ctx context.Context) (string, error) {
	var raw string
	e := r.db.QueryRowContext(ctx, "SELECT value FROM app_preferences WHERE key = ?", aisql.PreferenceKey).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return raw, e
}
func (r *AISQLRepository) CompareAndSwap(ctx context.Context, before, after string) error {
	var result sql.Result
	var e error
	if before == "" {
		result, e = r.db.ExecContext(ctx, "INSERT INTO app_preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE app_preferences.value = ''", aisql.PreferenceKey, after)
	} else {
		result, e = r.db.ExecContext(ctx, "UPDATE app_preferences SET value = ? WHERE key = ? AND value = ?", after, aisql.PreferenceKey, before)
	}
	if e != nil {
		return aisql.ErrStorage
	}
	n, e := result.RowsAffected()
	if e != nil {
		return aisql.ErrStorage
	}
	if n != 1 {
		return aisql.ErrConflict
	}
	return nil
}
