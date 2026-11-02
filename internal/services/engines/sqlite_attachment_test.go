package engines

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func createSQLiteAttachmentFixture(t *testing.T, path, text string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE shared(id INTEGER PRIMARY KEY,value TEXT,bin BLOB); CREATE INDEX shared_value ON shared(value); INSERT INTO shared VALUES(1,?,x'00ff');`, text); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteAttachmentsInitializeEveryPhysicalConnectionAndUseQualifiedMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	main, attached := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "space # % 数据.sqlite")
	createSQLiteAttachmentFixture(t, main, "main value")
	createSQLiteAttachmentFixture(t, attached, "attached value")
	a := connections.DefaultSQLiteOptions()
	a.BusyTimeoutMS = 37
	a.Attachments = []connections.SQLiteAttachment{{Name: `a"ux`, File: attached}}
	p := connections.Profile{ID: 81, Kind: connections.SQLite, Database: main, SQLiteOptions: a}
	m := NewManager()
	defer m.Close()
	if err := m.Connect(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Session(p.ID)
	p.SQLiteOptions.Attachments[0].Name = "later mutation"
	names, err := s.Namespaces(ctx)
	if err != nil || len(names) != 2 || names[1].Ref.Schema != `a"ux` {
		t.Fatalf("namespaces: %+v %v", names, err)
	}
	objects, err := s.Browse(ctx, names[1].Ref)
	if err != nil || len(objects) != 1 || objects[0].Ref.Catalog != "main" || objects[0].Ref.Schema != `a"ux` {
		t.Fatalf("objects: %+v %v", objects, err)
	}
	o := names[1].Ref.Object("table", "shared")
	page, err := s.Page(ctx, contract.PageRequest{Object: o, Limit: 20})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "attached value" || !page.Table.Editable || len(page.Table.Definitions) != 1 || page.Table.Definitions[0].Name != "shared_value" {
		t.Fatalf("qualified page: %+v %v", page, err)
	}
	// Holding leases simultaneously forces independent underlying connections.
	leases := make([]contract.SQLLease, 0, 4)
	defer func() {
		for _, l := range leases {
			l.Close()
		}
	}()
	for range 4 {
		l, err := s.Reserve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
		r, err := l.Execute(ctx, `SELECT value FROM "a""ux".shared; PRAGMA busy_timeout; PRAGMA foreign_keys;`)
		if err != nil || len(r.Sets) != 3 || r.Sets[0].Rows[0][0].Text != "attached value" || r.Sets[1].Rows[0][0].Text != "37" || r.Sets[2].Rows[0][0].Text != "1" {
			t.Fatalf("lease initialization: %+v %v", r, err)
		}
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	edit := contract.Edit{Kind: "update", Identity: page.Identities[0], Original: page.Rows[0], Values: map[string]contract.Cell{"value": {Kind: contract.Text, Text: "saved attachment"}}}
	if _, err = tx.Apply(ctx, o, []contract.Edit{edit}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	mainPage, err := s.Page(ctx, contract.PageRequest{Object: s.Namespace("main").Object("table", "shared"), Limit: 20})
	if err != nil || mainPage.Rows[0][1].Text != "main value" {
		t.Fatalf("write crossed namespace: %+v %v", mainPage, err)
	}
	if _, err := s.Structure(ctx, s.Namespace("unconfigured").Object("table", "shared")); err == nil {
		t.Fatal("accepted an unconfigured namespace")
	}
	for _, l := range leases {
		l.Close()
	}
	leases = nil
	if err = m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	p.SQLiteOptions = a
	// a shared the original slice with p; restore the original configured name.
	p.SQLiteOptions.Attachments = []connections.SQLiteAttachment{{Name: `a"ux`, File: attached}}
	if err = m.Connect(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Session(p.ID)
	restored, err := s.Page(ctx, contract.PageRequest{Object: s.Namespace(`a"ux`).Object("table", "shared"), Limit: 20})
	if err != nil || restored.Rows[0][1].Text != "saved attachment" || !reflect.DeepEqual(restored.Rows[0][2].Bytes, []byte{0, 255}) {
		t.Fatalf("reconnect: %+v %v", restored, err)
	}
}

func TestSQLiteReadOnlyOptionsBlockWritesAndMissingFilesNeverCreateAttachments(t *testing.T) {
	dir := t.TempDir()
	main, attached := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "attached.sqlite")
	createSQLiteAttachmentFixture(t, main, "main")
	createSQLiteAttachmentFixture(t, attached, "attached")
	for _, readOnlyMain := range []bool{false, true} {
		t.Run(map[bool]string{false: "readonly attachment", true: "readonly session"}[readOnlyMain], func(t *testing.T) {
			a := connections.DefaultSQLiteOptions()
			a.ReadOnly = readOnlyMain
			a.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: attached, ReadOnly: true}}
			m := NewManager()
			defer m.Close()
			if err := m.Connect(context.Background(), connections.Profile{ID: 82, Kind: connections.SQLite, Database: main, SQLiteOptions: a}, ""); err != nil {
				t.Fatal(err)
			}
			s, _ := m.Session(82)
			page, err := s.Page(context.Background(), contract.PageRequest{Object: s.Namespace("aux").Object("table", "shared"), Limit: 20})
			if err != nil || page.Table.Editable {
				t.Fatalf("read-only metadata: %+v %v", page, err)
			}
			l, err := s.Reserve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if _, err = l.Execute(context.Background(), "UPDATE aux.shared SET value='forbidden'"); err == nil {
				t.Fatal("read-only attachment accepted SQL write")
			}
			if readOnlyMain {
				if s.Capabilities().EditableTables {
					t.Fatal("read-only session advertises editing")
				}
				if _, err = l.Execute(context.Background(), "UPDATE main.shared SET value='forbidden'"); err == nil {
					t.Fatal("read-only main accepted SQL write")
				}
			}
		})
	}
	missing := filepath.Join(dir, "never-created.sqlite")
	a := connections.DefaultSQLiteOptions()
	a.RequireExisting = false
	a.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: missing}}
	if _, err := (sqliteDialect{}).open(connections.Profile{Database: filepath.Join(dir, "new-main.sqlite"), SQLiteOptions: a}, "", nil); err == nil {
		t.Fatal("accepted missing attachment")
	}
	for _, path := range []string{missing, filepath.Join(dir, "new-main.sqlite")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed initialization created a file: %s %v", path, err)
		}
	}
	a = connections.DefaultSQLiteOptions()
	if _, err := (sqliteDialect{}).open(connections.Profile{Database: missing, SQLiteOptions: a}, "", nil); err == nil {
		t.Fatal("accepted missing required main")
	}
	a.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: main}}
	if _, err := (sqliteDialect{}).open(connections.Profile{Database: main, SQLiteOptions: a}, "", nil); err == nil {
		t.Fatal("attached main twice")
	}
	link := filepath.Join(dir, "alias.sqlite")
	if err := os.Link(main, link); err == nil {
		a.Attachments[0].File = link
		if _, err := (sqliteDialect{}).open(connections.Profile{Database: main, SQLiteOptions: a}, "", nil); err == nil {
			t.Fatal("accepted same-file alias")
		}
	}
}
