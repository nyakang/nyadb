// Package localdb manages NyaDB's local, non-secret SQLite state.
package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 9

// Open initializes or migrates an on-disk configuration database.
// This database must never store passwords, tokens, or connection strings
// containing credentials.
func Open(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("localdb: empty path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("localdb: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, fmt.Errorf("localdb: create directory: %w", err)
	}
	// Opening with restrictive permissions prevents newly created profiles
	// from inheriting a permissive process umask. Do not alter existing files.
	f, err := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("localdb: create database: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("localdb: close database file: %w", err)
	}

	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, fmt.Errorf("localdb: open sqlite: %w", err)
	}
	// Keeps connection-local PRAGMA settings consistent and avoids writer
	// contention in the small local configuration database.
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("localdb: begin migration: %w", err)
	}
	defer tx.Rollback()

	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("localdb: read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("localdb: schema version %d is newer than supported %d", current, schemaVersion)
	}
	if current == 0 {
		// Deliberately no password, secret reference, or raw DSN fields.
		const create = "CREATE TABLE connection_profiles (" +
			"id INTEGER PRIMARY KEY AUTOINCREMENT," +
			"name TEXT NOT NULL UNIQUE," +
			"kind TEXT NOT NULL," +
			"host TEXT NOT NULL DEFAULT ''," +
			"port INTEGER NOT NULL DEFAULT 0," +
			"database_name TEXT NOT NULL DEFAULT ''," +
			"username TEXT NOT NULL DEFAULT ''," +
			"tls_mode TEXT NOT NULL DEFAULT ''," +
			"created_at_ms INTEGER NOT NULL," +
			"updated_at_ms INTEGER NOT NULL" +
			")"
		if _, err := tx.ExecContext(ctx, create); err != nil {
			return fmt.Errorf("localdb: migrate connection profiles: %w", err)
		}
		current = 1
	}
	if current == 1 {
		// Backward-compatible, non-secret JSON settings for MySQL only.
		// Empty values on existing profiles mean the default settings.
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE connection_profiles ADD COLUMN mysql_advanced_json TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("localdb: migrate MySQL advanced settings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 2
	}
	if current == 2 {
		// Notes belong to the profile irrespective of driver.
		// Upgraded profiles begin with empty notes.
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE connection_profiles ADD COLUMN notes TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("localdb: migrate connection notes: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 3
	}
	if current == 3 {
		// Additional MySQL configurations share a single parent connection.
		// Store only validated non-secret settings in one atomic JSON field.
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE connection_profiles ADD COLUMN variants_json TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return fmt.Errorf("localdb: migrate connection variants: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 4"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 4
	}
	if current == 4 {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE connection_profiles ADD COLUMN active_variant TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("localdb: migrate active configuration: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 5
	}
	if current == 5 {
		// Virtual groups contain navigation metadata, never database credentials.
		if _, err := tx.ExecContext(ctx, "CREATE TABLE connection_groups (name TEXT PRIMARY KEY COLLATE NOCASE)"); err != nil {
			return fmt.Errorf("localdb: create groups: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE connection_profiles ADD COLUMN group_name TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("localdb: add profile group: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 6"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 6
	}
	if current == 6 {
		// Star state belongs to the local connection profile, never to MySQL.
		if _, err := tx.ExecContext(ctx, "ALTER TABLE connection_profiles ADD COLUMN starred INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("localdb: add starred flag: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 7"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 7
	}
	if current == 7 {
		// Appearance and other non-secret preferences are app-local settings.
		if _, err := tx.ExecContext(ctx, "CREATE TABLE app_preferences (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			return fmt.Errorf("localdb: create preferences: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 8"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
		current = 8
	}
	if current == 8 {
		if _, err := tx.ExecContext(ctx, "CREATE TABLE sql_documents (id TEXT PRIMARY KEY, kind TEXT NOT NULL, profile_id INTEGER NOT NULL DEFAULT 0, format_version INTEGER NOT NULL, revision INTEGER NOT NULL, payload BLOB NOT NULL)"); err != nil {
			return fmt.Errorf("localdb: migrate encrypted SQL documents: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 9"); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("localdb: commit migration: %w", err)
	}
	return nil
}
