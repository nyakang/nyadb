package engines

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLiteFileURIKeepsAbsolutePathsOutOfAuthority(t *testing.T) {
	for _, path := range []string{
		"C:/Users/runner/AppData/Local/Temp/user.sqlite",
		"C:/data/space # ? % 数据.sqlite",
		"//server/share/data.sqlite",
		"/tmp/space # ? % 数据.sqlite",
		`/tmp/back\slash.sqlite`,
	} {
		t.Run(path, func(t *testing.T) {
			uri := sqliteFileURI(path)
			u, err := url.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
			want := path
			if !strings.HasPrefix(want, "/") {
				want = "/" + want
			}
			if u.Scheme != "file" || u.Host != "" || u.Path != want || u.Fragment != "" {
				t.Fatalf("file identity changed: %s => %+v", path, u)
			}
			pragmas := u.Query()["_pragma"]
			if len(pragmas) != 2 || pragmas[0] != "busy_timeout(1000)" || pragmas[1] != "foreign_keys(1)" {
				t.Fatalf("lost connection pragmas: %s", uri)
			}
		})
	}
}

func TestSQLiteFileURIReopensTheConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space # % 数据.sqlite")
	p := connections.Profile{Database: path}
	db, err := (sqliteDialect{}).open(p, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(context.Background(), "CREATE TABLE fixture (value TEXT); INSERT INTO fixture VALUES ('persisted')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("configured file was not created: %v", err)
	}
	reopened, err := (sqliteDialect{}).open(p, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value string
	if err = reopened.QueryRowContext(context.Background(), "SELECT value FROM fixture").Scan(&value); err != nil || value != "persisted" {
		t.Fatalf("reopened wrong file: %q, %v", value, err)
	}
}
