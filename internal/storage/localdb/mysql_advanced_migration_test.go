package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// An existing v1 profile must survive the v2 migration unchanged.
func TestMigrationFromV1PreservesProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	const v1 = `CREATE TABLE connection_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 0,
		database_name TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		tls_mode TEXT NOT NULL DEFAULT '',
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL
	)`
	if _, err := raw.Exec(v1); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO connection_profiles (name, kind, host, port, created_at_ms, updated_at_ms) VALUES ('prior', 'mysql', 'localhost', 3306, 1000, 1000)"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d (%v)", version, err)
	}
	var name, kind, advanced string
	if err := db.QueryRow("SELECT name, kind, mysql_advanced_json FROM connection_profiles WHERE id=1").Scan(&name, &kind, &advanced); err != nil {
		t.Fatal(err)
	}
	if name != "prior" || kind != "mysql" || advanced != "" {
		t.Fatalf("migration modified existing record: %q %q %q", name, kind, advanced)
	}
	// Reopening the migrated file must not add a second column.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var columns int
	if err := db2.QueryRow("SELECT COUNT(*) FROM pragma_table_info('connection_profiles') WHERE name='mysql_advanced_json'").Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("unexpected advanced settings columns: %d %v", columns, err)
	}
}
