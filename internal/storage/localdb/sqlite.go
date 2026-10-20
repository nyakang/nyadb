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

const schemaVersion = 1

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
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("localdb: set schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("localdb: commit migration: %w", err)
	}
	return nil
}
