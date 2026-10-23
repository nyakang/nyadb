package localdb

import (
	"database/sql"
	"errors"
	"fmt"
)

// Preference returns a non-secret application setting. A missing key is empty.
func Preference(db *sql.DB, key string) (string, error) {
	if key == "" {
		return "", errors.New("localdb: empty preference key")
	}
	var value string
	err := db.QueryRow("SELECT value FROM app_preferences WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("localdb: read preference: %w", err)
	}
	return value, nil
}

// SetPreference persists application settings without modifying connections.
func SetPreference(db *sql.DB, key, value string) error {
	if key == "" {
		return errors.New("localdb: empty preference key")
	}
	_, err := db.Exec("INSERT INTO app_preferences (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	if err != nil {
		return fmt.Errorf("localdb: write preference: %w", err)
	}
	return nil
}
