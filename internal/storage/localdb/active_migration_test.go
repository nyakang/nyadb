package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationV4ToV5PreservesVariantsAndDefaultsPrimaryActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE connection_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0,
		database_name TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
		tls_mode TEXT NOT NULL DEFAULT '', mysql_advanced_json TEXT NOT NULL DEFAULT '',
		notes TEXT NOT NULL DEFAULT '', variants_json TEXT NOT NULL DEFAULT '[]',
		created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	const oldVariants = `[{"Name":"Replica","Host":"replica.test","Port":3307}]`
	if _, err := raw.Exec("INSERT INTO connection_profiles (name, kind, host, port, variants_json, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, 1, 1)", "Primary", "mysql", "localhost", 3306, oldVariants); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("version: %d %v", version, err)
	}
	var variants, active string
	if err := db.QueryRow("SELECT variants_json, active_variant FROM connection_profiles WHERE id=1").Scan(&variants, &active); err != nil {
		t.Fatal(err)
	}
	if variants != oldVariants || active != "" {
		t.Fatalf("existing configurations lost or wrong Active default: %q %q", variants, active)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('connection_profiles') WHERE name='active_variant'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("non-idempotent migration: %d %v", count, err)
	}
}
