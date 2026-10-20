package localdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenMigrationIdempotentAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.db")
	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
			t.Fatalf("schema version=%d err=%v", version, err)
		}
		db.Close()
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows uses ACLs; os.FileMode permission bits do not reflect access
	// rights there. Keep the POSIX privacy assertion on Unix platforms.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("database permissions too broad: %v", info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(connection_profiles)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index, notNull, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&index, &name, &kind, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "dsn") {
			t.Fatalf("secret-bearing schema column: %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectUnknownSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("newer schema must not be silently overwritten")
	}
}

func TestRejectEmptyPath(t *testing.T) {
	if db, err := Open("  "); err == nil {
		db.Close()
		t.Fatal("empty database path accepted")
	}
}
