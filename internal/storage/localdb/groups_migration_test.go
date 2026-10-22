package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationV5ToV6PreservesConnectionsAndAddsEmptyGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-v5.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE connection_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL, host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0,
		database_name TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
		tls_mode TEXT NOT NULL DEFAULT '', mysql_advanced_json TEXT NOT NULL DEFAULT '',
		notes TEXT NOT NULL DEFAULT '', variants_json TEXT NOT NULL DEFAULT '[]',
		active_variant TEXT NOT NULL DEFAULT '', created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec("INSERT INTO connection_profiles (name, kind, host, port, notes, created_at_ms, updated_at_ms) VALUES ('old', 'mysql', 'localhost', 3306, 'keep notes', 1, 1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var group, notes string
	if err := db.QueryRow("SELECT group_name, notes FROM connection_profiles WHERE name='old'").Scan(&group, &notes); err != nil || group != "" || notes != "keep notes" {
		t.Fatalf("existing profile changed: group=%q notes=%q err=%v", group, notes, err)
	}
	if _, err := db.Exec("INSERT INTO connection_groups(name) VALUES ('开发库')"); err != nil {
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
	if err := reopened.QueryRow("SELECT COUNT(*) FROM connection_groups WHERE name='开发库'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("group migration not idempotent: count=%d err=%v", count, err)
	}
}
