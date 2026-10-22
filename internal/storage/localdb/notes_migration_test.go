package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationV2ToV3AddsNotesWithoutChangingProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE connection_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 0,
		database_name TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		tls_mode TEXT NOT NULL DEFAULT '',
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL,
		mysql_advanced_json TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	const advanced = `{"clientDriver":"默认","charset":"自动"}`
	_, err = raw.Exec("INSERT INTO connection_profiles (name, kind, host, port, mysql_advanced_json, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"prior", "mysql", "localhost", 3306, advanced, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("schema version %d (%v)", version, err)
	}
	var name, savedAdvanced, notes string
	if err := db.QueryRow("SELECT name, mysql_advanced_json, notes FROM connection_profiles WHERE id=1").
		Scan(&name, &savedAdvanced, &notes); err != nil {
		t.Fatal(err)
	}
	if name != "prior" || savedAdvanced != advanced || notes != "" {
		t.Fatalf("migration altered record: %q %q %q", name, savedAdvanced, notes)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET notes='draft\ntext' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.QueryRow("SELECT COUNT(*) FROM pragma_table_info('connection_profiles') WHERE name='notes'").
		Scan(&count); err != nil || count != 1 {
		t.Fatalf("notes column added more than once: %d %v", count, err)
	}
	if err := reopened.QueryRow("SELECT notes FROM connection_profiles WHERE id=1").Scan(&notes); err != nil || notes != "draft\ntext" {
		t.Fatalf("notes did not survive reopening: %q %v", notes, err)
	}
}
