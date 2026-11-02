package engines

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLiteDropVerifiedCommitRemovesOnlyOriginalTableAndOwnDefinitions(t *testing.T) {
	s := sqliteDesignFixture(t, `CREATE TABLE "t; quoted"(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT); CREATE INDEX idx ON "t; quoted"(value); CREATE TABLE audit(value); CREATE TRIGGER tr AFTER DELETE ON "t; quoted" BEGIN INSERT INTO audit VALUES(old.value); END; INSERT INTO "t; quoted" VALUES(91,'private value');`)
	original := designSnapshot(t, s, "main", "t; quoted")
	out := s.SchemaDesign().Drop(context.Background(), original)
	requireDesignCommit(t, out)
	if out.Actual.Target != original.Target || out.Actual.Schema.Exists {
		t.Fatalf("drop receipt: %+v", out)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN ('t; quoted','idx','tr')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("original objects remain: %d %v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil || count != 0 {
		t.Fatalf("drop fired delete trigger: %d %v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_sequence WHERE name='t; quoted'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("own sequence remains: %d %v", count, err)
	}
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&count); err != nil || count != 1 {
		t.Fatalf("PRAGMA not restored: %d %v", count, err)
	}
}

func TestSQLiteDropRefusesIncomingReferencesAndRollsBackProgramsWithoutSideEffects(t *testing.T) {
	for _, item := range []struct{ name, ddl string }{
		{"empty foreign key", `CREATE TABLE child(id INTEGER PRIMARY KEY,parent REFERENCES T(id) ON DELETE CASCADE);`},
		{"cascade rows", `CREATE TABLE child(id INTEGER PRIMARY KEY,parent REFERENCES t(id) ON DELETE CASCADE); INSERT INTO child VALUES(1,7);`},
		{"view", `CREATE VIEW v AS SELECT * FROM t;`},
		{"view chain", `CREATE VIEW v AS SELECT * FROM t; CREATE VIEW outer_v AS SELECT * FROM v;`},
		{"insert trigger", `CREATE TABLE other(value); CREATE TRIGGER other_insert AFTER INSERT ON other BEGIN INSERT INTO t VALUES(new.value,'trigger'); END;`},
		{"false when trigger", `CREATE TABLE other(value); CREATE TRIGGER other_insert AFTER INSERT ON other WHEN 0 BEGIN DELETE FROM t; END;`},
		{"update trigger", `CREATE TABLE other(value); CREATE TRIGGER other_update AFTER UPDATE OF value ON other BEGIN UPDATE t SET value='changed'; END;`},
		{"delete trigger", `CREATE TABLE other(value); CREATE TRIGGER other_delete AFTER DELETE ON other BEGIN DELETE FROM t; END;`},
		{"view trigger", `CREATE TABLE other(value); CREATE VIEW writable AS SELECT * FROM other; CREATE TRIGGER writable_insert INSTEAD OF INSERT ON writable BEGIN INSERT INTO t VALUES(new.value,'trigger'); END;`},
		{"view update trigger", `CREATE TABLE other(value); CREATE VIEW writable AS SELECT * FROM other; CREATE TRIGGER writable_update INSTEAD OF UPDATE OF value ON writable BEGIN UPDATE t SET value='changed'; END;`},
		{"unreported view column", `CREATE TABLE other(value); CREATE VIEW writable AS SELECT * FROM other; CREATE TRIGGER writable_update INSTEAD OF UPDATE OF nonexistent ON writable BEGIN DELETE FROM t; END;`},
		{"view delete trigger", `CREATE TABLE other(value); CREATE VIEW writable AS SELECT * FROM other; CREATE TRIGGER writable_delete INSTEAD OF DELETE ON writable BEGIN DELETE FROM t; END;`},
		{"sqlite prefix lookalike", `CREATE TABLE sqliteXother(value); CREATE TRIGGER sqliteXtrigger AFTER INSERT ON sqliteXother BEGIN DELETE FROM t; END;`},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := sqliteDesignFixture(t, `CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(7,'saved');`+item.ddl)
			original := designSnapshot(t, s, "main", "t")
			out := s.SchemaDesign().Drop(context.Background(), original)
			if out.State != "rolled_back" || out.Verified || out.Error == nil {
				t.Fatalf("unsafe drop: %+v", out)
			}
			actual := designSnapshot(t, s, "main", "t")
			if schemaddl.Signature(actual) != schemaddl.Signature(original) {
				t.Fatal("rollback changed schema")
			}
			var value string
			if err := s.db.QueryRow("SELECT value FROM t WHERE id=7").Scan(&value); err != nil || value != "saved" {
				t.Fatalf("rollback lost source data: %s %v", value, err)
			}
			if item.name == "cascade rows" {
				var count int
				if err := s.db.QueryRow("SELECT count(*) FROM child").Scan(&count); err != nil || count != 1 {
					t.Fatalf("cascade side effect: %d %v", count, err)
				}
			}
		})
	}
}

func TestSQLiteDropAllowsSelfReferencesAndUnrelatedViewsAndTriggers(t *testing.T) {
	s := sqliteDesignFixture(t, `CREATE TABLE t(id INTEGER PRIMARY KEY,parent REFERENCES t(id)); INSERT INTO t VALUES(1,1); CREATE TABLE other(value); CREATE TABLE audit(value); CREATE VIEW v AS SELECT value FROM other; CREATE TRIGGER tr AFTER INSERT ON other BEGIN INSERT INTO audit VALUES('t; literal'); END;`)
	requireDesignCommit(t, s.SchemaDesign().Drop(context.Background(), designSnapshot(t, s, "main", "t")))
	if _, err := s.db.Exec("INSERT INTO other VALUES(3)"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := s.db.QueryRow("SELECT value FROM audit").Scan(&value); err != nil || value != "t; literal" {
		t.Fatalf("unrelated trigger changed: %s %v", value, err)
	}
}

func TestSQLiteDropRejectsRevisionIdentityAndForgedSnapshots(t *testing.T) {
	for _, change := range []string{"namespace", "replacement", "forged", "profile", "generation", "absent"} {
		t.Run(change, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(1,'saved')")
			original := designSnapshot(t, s, "main", "t")
			switch change {
			case "namespace":
				_, _ = s.db.Exec("CREATE TABLE other(id)")
			case "replacement":
				_, _ = s.db.Exec("DROP TABLE t; CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(2,'replacement')")
			case "forged":
				original.Schema.Table.Clauses[0].Name = "forged"
			case "profile":
				original.Target.Object.ProfileID++
			case "generation":
				original.Target.Generation++
			case "absent":
				original.Schema.Exists = false
			}
			out := s.SchemaDesign().Drop(context.Background(), original)
			if out.Error == nil || out.Verified || out.State == "committed" {
				t.Fatalf("unsafe snapshot accepted: %+v", out)
			}
			if change == "namespace" || change == "replacement" || change == "forged" {
				if !errors.Is(out.Error, schemaddl.ErrConflict) {
					t.Fatal(out.Error)
				}
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM t").Scan(&count); err != nil || count != 1 {
				t.Fatalf("conflict changed original: %d %v", count, err)
			}
		})
	}
}

func TestSQLiteDropAttachedNamespaceAndReadOnlyBinding(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "read-only"}[readOnly], func(t *testing.T) {
			dir := t.TempDir()
			main, attached := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "attached.sqlite")
			createSQLiteAttachmentFixture(t, main, "main")
			createSQLiteAttachmentFixture(t, attached, "attached")
			opts := connections.DefaultSQLiteOptions()
			opts.Attachments = []connections.SQLiteAttachment{{Name: `a"ux`, File: attached, ReadOnly: readOnly}}
			m := NewManager()
			defer m.Close()
			p := connections.Profile{ID: 83, Kind: connections.SQLite, Database: main, SQLiteOptions: opts}
			if err := m.Connect(context.Background(), p, ""); err != nil {
				t.Fatal(err)
			}
			s, _ := m.Session(p.ID)
			original := designSnapshot(t, s, `a"ux`, "shared")
			ops := s.SchemaDesign()
			if ops.CanDrop(original.Target) == readOnly {
				t.Fatal("wrong read-only availability")
			}
			out := ops.Drop(context.Background(), original)
			if readOnly {
				if out.Error == nil || out.State != "not_executed" {
					t.Fatalf("read-only drop: %+v", out)
				}
			} else {
				requireDesignCommit(t, out)
			}
			var value string
			if err := s.db.QueryRow("SELECT value FROM main.shared").Scan(&value); err != nil || value != "main" {
				t.Fatalf("drop crossed namespace: %s %v", value, err)
			}
		})
	}
}

func TestSQLiteDropCancelsRealLockWaitAndRetainsSource(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(1,'saved')")
	original := designSnapshot(t, s, "main", "t")
	blocker, err := sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(s.catalog)))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec("ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out := s.SchemaDesign().Drop(ctx, original)
	if out.Verified || out.State == "committed" || out.Error == nil {
		t.Fatalf("cancelled lock wait committed: %+v", out)
	}
	if ctx.Err() == nil {
		t.Fatal("fixture did not wait for a lock")
	}
	if _, err = blocker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	actual := designSnapshot(t, s, "main", "t")
	if schemaddl.Signature(actual) != schemaddl.Signature(original) {
		t.Fatal("lock cancellation changed source")
	}
	requireDesignCommit(t, s.SchemaDesign().Drop(context.Background(), actual))
}

func TestSQLiteDropRejectsOwnedTransactionsAndCrossNamespaceTemporaryPrograms(t *testing.T) {
	for _, sql := range []string{"BEGIN IMMEDIATE", "CREATE TEMP VIEW temp_view AS SELECT * FROM main.t", "CREATE TEMP TRIGGER temp_trigger AFTER INSERT ON main.other BEGIN DELETE FROM t; END"} {
		t.Run(sql, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY); CREATE TABLE other(id)")
			s.db.SetMaxOpenConns(2)
			original := designSnapshot(t, s, "main", "t")
			l, err := s.Reserve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if _, err = l.Execute(context.Background(), sql); err != nil {
				t.Fatal(err)
			}
			out := s.SchemaDesign().Drop(context.Background(), original)
			if out.State != "not_executed" || out.Error == nil {
				t.Fatalf("unsafe leased state: %+v", out)
			}
			if strings.HasPrefix(sql, "BEGIN") {
				if _, err = l.Execute(context.Background(), "COMMIT"); err != nil {
					t.Fatal("drop blocked user commit", err)
				}
			}
		})
	}
}

var _ contract.SchemaDesignProvider = (*Session)(nil)
