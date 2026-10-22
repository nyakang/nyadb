package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationV3ToV4AddsConfigurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE connection_profiles (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, host TEXT NOT NULL DEFAULT '',
	port INTEGER NOT NULL DEFAULT 0, database_name TEXT NOT NULL DEFAULT '',
	username TEXT NOT NULL DEFAULT '', tls_mode TEXT NOT NULL DEFAULT '',
	created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL,
	mysql_advanced_json TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO connection_profiles (name, kind, host, port, notes, created_at_ms, updated_at_ms) VALUES ('old', 'mysql', 'localhost', 3306, 'saved note', 1, 1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := opened.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("unexpected version %d %v", version, err)
	}
	var name, host, notes, variants string
	if err := opened.QueryRow("SELECT name, host, notes, variants_json FROM connection_profiles").Scan(&name, &host, &notes, &variants); err != nil {
		t.Fatal(err)
	}
	if name != "old" || host != "localhost" || notes != "saved note" || variants != "[]" {
		t.Fatalf("migration changed existing settings: %q %q %q %q", name, host, notes, variants)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var columns int
	if err := reopened.QueryRow("SELECT COUNT(*) FROM pragma_table_info('connection_profiles') WHERE name='variants_json'").Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("migration not idempotent: %d %v", columns, err)
	}
}
