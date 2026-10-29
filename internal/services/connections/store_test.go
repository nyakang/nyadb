package connections_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func openStore(t *testing.T) (*localdb.ProfileStore, string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nyadb", "config.db")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return localdb.NewProfileStore(db), path, db
}
