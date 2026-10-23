package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationV6ToV7PreservesConnectionGroupsAndAddsFavorites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE connection_profiles (
  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,host TEXT NOT NULL DEFAULT '',port INTEGER NOT NULL DEFAULT 0,
  database_name TEXT NOT NULL DEFAULT '',username TEXT NOT NULL DEFAULT '',
  tls_mode TEXT NOT NULL DEFAULT '',mysql_advanced_json TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',variants_json TEXT NOT NULL DEFAULT '[]',
  active_variant TEXT NOT NULL DEFAULT '',created_at_ms INTEGER NOT NULL,
  updated_at_ms INTEGER NOT NULL, group_name TEXT NOT NULL DEFAULT '');
  CREATE TABLE connection_groups(name TEXT PRIMARY KEY COLLATE NOCASE);
  INSERT INTO connection_groups(name) VALUES('开发库');
  INSERT INTO connection_profiles(name,kind,host,port,created_at_ms,updated_at_ms,group_name)
  VALUES('old','mysql','localhost',3306,1,1,'开发库');
  PRAGMA user_version=6;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var version, star int
	var name, group string
	if err = opened.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = opened.QueryRow("SELECT name,group_name,starred FROM connection_profiles WHERE id=1").
		Scan(&name, &group, &star); err != nil || name != "old" || group != "开发库" || star != 0 {
		t.Fatalf("profile migrated: %q %q star=%d err=%v", name, group, star, err)
	}
	if _, err = opened.Exec("UPDATE connection_profiles SET starred=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
}
