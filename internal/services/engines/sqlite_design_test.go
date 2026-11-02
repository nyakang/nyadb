package engines

import (
	"bytes"
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

func sqliteDesignFixture(t *testing.T, ddl string) *Session {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	p := connections.Profile{ID: 71, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "design.sqlite")}
	if err := m.Connect(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Session(p.ID)
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return s
}
func designSnapshot(t *testing.T, s *Session, schema, table string) contract.SchemaDesignSnapshot {
	t.Helper()
	snapshot, err := s.SchemaDesign().Snapshot(context.Background(), contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("table", table), Generation: s.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func addSQLiteColumn(t *testing.T, snapshot contract.SchemaDesignSnapshot, name, sql string) contract.SchemaDesignRequest {
	t.Helper()
	desired := snapshot.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:" + name, Name: name, Kind: "column", SQL: sql})
	return contract.SchemaDesignRequest{Original: snapshot, Desired: desired}
}
func requireDesignCommit(t *testing.T, out contract.SchemaDesignOutcome) {
	t.Helper()
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatalf("design commit: state=%s verified=%v error=%v", out.State, out.Verified, out.Error)
	}
}
func noSQLiteShadow(t *testing.T, s *Session) {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE '__nyadb_rebuild_%'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("shadow leaked: %d %v", count, err)
	}
}

func TestSQLiteRebuildPreservesFullSchemaTypedDataSequenceAndDependencies(t *testing.T) {
	s := sqliteDesignFixture(t, `
CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT COLLATE NOCASE, raw BLOB, n INTEGER, amount TEXT, gen TEXT GENERATED ALWAYS AS (label || ':' || n) STORED, CHECK (n >= 0));
CREATE UNIQUE INDEX items_label ON items(label COLLATE BINARY) WHERE label IS NOT NULL;
CREATE TABLE audit(value TEXT);
CREATE TRIGGER items_update AFTER UPDATE ON items BEGIN INSERT INTO audit VALUES(CASE WHEN new.n>0 THEN new.label ELSE 'zero; END' END); END;
CREATE VIEW item_view AS SELECT id,label,raw,amount,gen FROM items;
CREATE TABLE child(id INTEGER PRIMARY KEY, item_id INTEGER REFERENCES items(id) ON DELETE CASCADE);
INSERT INTO items VALUES(7,'Label',x'00ff',9223372036854775806,'12345678901234567890.123456789');
INSERT INTO items VALUES(8,NULL,NULL,0,NULL);
INSERT INTO items VALUES(100,'deleted high water',x'01',1,'0');
DELETE FROM items WHERE id=100;
INSERT INTO child VALUES(1,7);
`)
	payload := bytes.Repeat([]byte{0, 255, 17, 128}, (1<<20)/4+1)
	if _, err := s.db.Exec("INSERT INTO items(id,label,raw,n,amount) VALUES(9,'large blob',?,4,'0.000000000000000000001')", payload); err != nil {
		t.Fatal(err)
	}
	original := designSnapshot(t, s, "main", "items")
	out := s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT DEFAULT 'new value'"))
	requireDesignCommit(t, out)
	if out.Rows != 3 || len(out.Actual.Schema.Table.Definitions) != 2 {
		t.Fatalf("incomplete rebuilt structure: rows=%d defs=%d", out.Rows, len(out.Actual.Schema.Table.Definitions))
	}
	var label, amount, generated, added string
	var n int64
	var raw []byte
	if err := s.db.QueryRow("SELECT label,raw,n,amount,gen,added FROM items WHERE id=7").Scan(&label, &raw, &n, &amount, &generated, &added); err != nil || label != "Label" || !bytes.Equal(raw, []byte{0, 255}) || n != 9223372036854775806 || amount != "12345678901234567890.123456789" || generated != "Label:9223372036854775806" || added != "new value" {
		t.Fatalf("retained values changed: %s %d %s %s %s %v", label, n, amount, generated, added, err)
	}
	if err := s.db.QueryRow("SELECT raw FROM items WHERE id=9").Scan(&raw); err != nil || !bytes.Equal(raw, payload) {
		t.Fatalf("large blob changed: %v", err)
	}
	var nulls, child, audit, views int
	if err := s.db.QueryRow("SELECT label IS NULL AND raw IS NULL AND amount IS NULL AND gen IS NULL FROM items WHERE id=8").Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("NULL changed: %d %v", nulls, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM child WHERE item_id=7").Scan(&child); err != nil || child != 1 {
		t.Fatalf("FK cascade during rebuild: %d %v", child, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM audit").Scan(&audit); err != nil || audit != 0 {
		t.Fatalf("trigger executed during rebuild: %d %v", audit, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM item_view").Scan(&views); err != nil || views != 3 {
		t.Fatalf("view lost: %d %v", views, err)
	}
	var seq int64
	if err := s.db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='items'").Scan(&seq); err != nil || seq != 100 {
		t.Fatalf("sequence regressed: %d %v", seq, err)
	}
	if _, err := s.db.Exec("INSERT INTO items(label,n) VALUES('next',1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT id FROM items WHERE label='next'").Scan(&seq); err != nil || seq != 101 {
		t.Fatalf("next ID: %d %v", seq, err)
	}
	if _, err := s.db.Exec("UPDATE items SET label='updated' WHERE id=7"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT value FROM audit").Scan(&label); err != nil || label != "updated" {
		t.Fatalf("trigger lost: %s %v", label, err)
	}
	noSQLiteShadow(t, s)
}

func TestSQLiteRebuildRollbackOnValueConversionAndInvalidDependencies(t *testing.T) {
	for _, kind := range []string{"affinity", "constraint", "view", "trigger", "update of", "generated conversion"} {
		t.Run(kind, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(9,'001');")
			if kind == "view" {
				if _, err := s.db.Exec("CREATE VIEW v AS SELECT value FROM t"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "trigger" {
				if _, err := s.db.Exec("CREATE TABLE log(value); CREATE TRIGGER tr AFTER UPDATE ON t BEGIN INSERT INTO log VALUES(new.value); END;"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "update of" {
				if _, err := s.db.Exec("CREATE TABLE log(value); CREATE TRIGGER tr AFTER UPDATE OF value ON t BEGIN INSERT INTO log VALUES(1); END;"); err != nil {
					t.Fatal(err)
				}
			}
			original := designSnapshot(t, s, "main", "t")
			desired := original.Schema.Table.Clone()
			switch kind {
			case "affinity":
				desired.Clauses[1].SQL = "INTEGER"
			case "constraint":
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "check", Kind: "constraint", SQL: "CHECK (value='other')"})
			case "generated conversion":
				desired.Clauses[1].SQL = "TEXT GENERATED ALWAYS AS (id || 'computed') STORED"
			case "view", "trigger", "update of":
				desired.Clauses = desired.Clauses[:1]
			}
			out := s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
			if out.State != "rolled_back" || out.Error == nil || out.Verified {
				t.Fatalf("unsafe change: %+v", out)
			}
			if kind == "affinity" && !errors.Is(out.Error, schemaddl.ErrDataChanged) {
				t.Fatal(out.Error)
			}
			var id int64
			var value, typ string
			if err := s.db.QueryRow("SELECT id,value,typeof(value) FROM t").Scan(&id, &value, &typ); err != nil || id != 9 || value != "001" || typ != "text" {
				t.Fatalf("rollback lost data: %d %s %s %v", id, value, typ, err)
			}
			noSQLiteShadow(t, s)
			var fk, legacy int
			if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("PRAGMA legacy_alter_table").Scan(&legacy); err != nil || fk != 1 || legacy != 0 {
				t.Fatalf("connection state: %d %d %v", fk, legacy, err)
			}
		})
	}
}

func TestSQLiteDesignAttachedNamespaceAndReadOnlyIsolation(t *testing.T) {
	dir := t.TempDir()
	main, attached := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "attached.sqlite")
	createSQLiteAttachmentFixture(t, main, "main value")
	createSQLiteAttachmentFixture(t, attached, "attached value")
	options := connections.DefaultSQLiteOptions()
	options.Attachments = []connections.SQLiteAttachment{{Name: `a"ux`, File: attached}}
	m := NewManager()
	defer m.Close()
	p := connections.Profile{ID: 81, Kind: connections.SQLite, Database: main, SQLiteOptions: options}
	if err := m.Connect(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Session(p.ID)
	s.db.SetMaxOpenConns(1)
	original := designSnapshot(t, s, `a"ux`, "shared")
	r := addSQLiteColumn(t, original, "added", "TEXT DEFAULT 'attached addition'")
	r.Desired.Definitions = append(r.Desired.Definitions, schemaddl.Definition{Name: "attached_index", Kind: "index", SQL: `CREATE INDEX "a""ux".attached_index ON shared(added)`})
	out := s.SchemaDesign().Apply(context.Background(), r)
	requireDesignCommit(t, out)
	var value, added string
	if err := s.db.QueryRow(`SELECT value,added FROM "a""ux".shared`).Scan(&value, &added); err != nil || value != "attached value" || added != "attached addition" {
		t.Fatalf("attached change: %s %s %v", value, added, err)
	}
	var mainColumns int
	if err := s.db.QueryRow("SELECT count(*) FROM pragma_table_xinfo('shared','main')").Scan(&mainColumns); err != nil || mainColumns != 3 {
		t.Fatalf("main table changed: %d %v", mainColumns, err)
	}
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	p.SQLiteOptions.Attachments[0].ReadOnly = true
	if err := m.Connect(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Session(p.ID)
	original = designSnapshot(t, s, `a"ux`, "shared")
	out = s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "forbidden", "TEXT"))
	if out.State != "not_executed" || out.Error == nil {
		t.Fatalf("read-only namespace write: %+v", out)
	}
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	p.SQLiteOptions.ReadOnly = true
	if err := m.Connect(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Session(p.ID)
	original = designSnapshot(t, s, "main", "shared")
	out = s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "forbidden", "TEXT"))
	if out.State != "not_executed" || out.Error == nil {
		t.Fatalf("read-only session write: %+v", out)
	}
}

func TestSQLiteDesignRetainsEmptyAutoincrementHighWaterAndAllowsConfirmedDrop(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT); INSERT INTO t VALUES(200,'removed'); DELETE FROM t;")
	original := designSnapshot(t, s, "main", "t")
	out := s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT"))
	requireDesignCommit(t, out)
	if out.Rows != 0 {
		t.Fatal(out.Rows)
	}
	if _, err := s.db.Exec("INSERT INTO t(value) VALUES('new')"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.db.QueryRow("SELECT id FROM t").Scan(&id); err != nil || id != 201 {
		t.Fatalf("empty table sequence: %d %v", id, err)
	}
	original = designSnapshot(t, s, "main", "t")
	desired := original.Schema.Table.Clone()
	desired.Clauses = desired.Clauses[:2]
	out = s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "not_executed" || out.Error == nil {
		t.Fatalf("drop bypass: %+v", out)
	}
	out = s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
	requireDesignCommit(t, out)
	if len(out.Actual.Schema.Table.Columns()) != 2 {
		t.Fatal(out.Actual.Schema.Table)
	}
}

func TestSQLiteDesignRejectsHiddenRowIDLossAndNeverExecutesInjectedStatements(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(rowid TEXT,_rowid_ TEXT,oid TEXT); CREATE TABLE keep(value)")
	original := designSnapshot(t, s, "main", "t")
	out := s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT"))
	if out.State != "rolled_back" || out.Error == nil {
		t.Fatalf("unaddressable identity: %+v", out)
	}
	desired := original.Schema.Table.Clone()
	desired.Clauses[0].SQL = "TEXT); DROP TABLE keep; --"
	out = s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "not_executed" || out.Error == nil {
		t.Fatalf("SQL boundary: %+v", out)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='keep'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("injected statement executed: %d %v", count, err)
	}
	noSQLiteShadow(t, s)
}

func TestSQLiteSchemaRevisionRejectsLateMutationAndQualifiedCrossProfileRequests(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,v TEXT)")
	original := designSnapshot(t, s, "main", "t")
	r := addSQLiteColumn(t, original, "added", "TEXT")
	if _, err := s.db.Exec("CREATE TABLE unrelated(x)"); err != nil {
		t.Fatal(err)
	}
	out := s.SchemaDesign().Apply(context.Background(), r)
	if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) {
		t.Fatalf("late schema change: %+v", out)
	}
	r.Original = designSnapshot(t, s, "main", "t")
	r.Original.Target.Generation++
	if out = s.SchemaDesign().Apply(context.Background(), r); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrRevoked) {
		t.Fatalf("generation: %+v", out)
	}
	r.Original.Target.Generation = s.Generation()
	r.Original.Target.Object.ProfileID++
	if out = s.SchemaDesign().Apply(context.Background(), r); out.State != "not_executed" || out.Error == nil {
		t.Fatalf("profile: %+v", out)
	}
	noSQLiteShadow(t, s)
}

func TestSQLiteNativeRenameRewritesForeignKeysViewsAndTriggers(t *testing.T) {
	s := sqliteDesignFixture(t, `CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(3,'saved'); CREATE VIEW v AS SELECT value FROM t; CREATE INDEX ix ON t(value); CREATE TABLE log(value TEXT); CREATE TRIGGER tr AFTER UPDATE ON t BEGIN INSERT INTO log VALUES(new.value); END; CREATE TABLE child(id INTEGER REFERENCES t(id)); INSERT INTO child VALUES(3); PRAGMA foreign_keys=OFF; PRAGMA legacy_alter_table=ON;`)
	original := designSnapshot(t, s, "main", "t")
	desired := original.Schema.Table.Clone()
	desired.Name = "renamed table"
	out := s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	requireDesignCommit(t, out)
	var value string
	if err := s.db.QueryRow("SELECT value FROM v").Scan(&value); err != nil || value != "saved" {
		t.Fatalf("renamed view: %s %v", value, err)
	}
	original = *out.Actual
	desired = original.Schema.Table.Clone()
	desired.Clauses[1].Name = "label"
	out = s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	requireDesignCommit(t, out)
	if _, err := s.db.Exec(`UPDATE "renamed table" SET label='changed'`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT value FROM log").Scan(&value); err != nil || value != "changed" {
		t.Fatalf("trigger rename: %s %v", value, err)
	}
	var sqlText string
	if err := s.db.QueryRow("SELECT sql FROM sqlite_schema WHERE name='child'").Scan(&sqlText); err != nil || !strings.Contains(sqlText, `"renamed table"`) {
		t.Fatalf("FK target: %s %v", sqlText, err)
	}
	var fk, legacy int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA legacy_alter_table").Scan(&legacy); err != nil || fk != 0 || legacy != 1 {
		t.Fatalf("restoration: %d %d %v", fk, legacy, err)
	}
}

func TestSQLiteRebuildWithoutRowIDAndHiddenIdentityWithDuplicates(t *testing.T) {
	for _, without := range []bool{false, true} {
		t.Run(map[bool]string{false: "rowid duplicates", true: "without rowid"}[without], func(t *testing.T) {
			ddl := "CREATE TABLE t(key TEXT,value BLOB); INSERT INTO t(rowid,key,value) VALUES(10,'same',x'00ff'),(80,'same',x'00ff');"
			if without {
				ddl = "CREATE TABLE t(key TEXT PRIMARY KEY,value BLOB) WITHOUT ROWID; INSERT INTO t VALUES('a',x'00ff'),('b',NULL);"
			}
			s := sqliteDesignFixture(t, ddl)
			original := designSnapshot(t, s, "main", "t")
			out := s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT DEFAULT 'new'"))
			requireDesignCommit(t, out)
			if out.Rows != 2 {
				t.Fatal(out.Rows)
			}
			if !without {
				var ids string
				if err := s.db.QueryRow("SELECT group_concat(rowid,',') FROM t ORDER BY rowid").Scan(&ids); err != nil || ids != "10,80" {
					t.Fatalf("rowid changed: %s %v", ids, err)
				}
			}
			noSQLiteShadow(t, s)
		})
	}
}

func TestSQLiteDesignCancelledLockWaitNeverLeavesTransactionOrChangesTable(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
	original := designSnapshot(t, s, "main", "t")
	other, err := sql.Open("sqlite", sqliteFileURI(s.catalog))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer other.Exec("ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out := s.SchemaDesign().Apply(ctx, addSQLiteColumn(t, original, "added", "TEXT"))
	if out.Error == nil || out.State != "not_executed" || out.Verified {
		t.Fatalf("lock cancellation: %+v", out)
	}
	if _, err = other.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	noSQLiteShadow(t, s)
	if _, err = s.db.Exec("INSERT INTO t VALUES(1,'still works')"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteDesignNewTableAndTemporaryTriggerSafety(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
	original := designSnapshot(t, s, "main", "new table")
	desired, err := schemaddl.ParseSQLite(`CREATE TABLE "new table"(id INTEGER PRIMARY KEY, raw BLOB) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	desired.Definitions = []schemaddl.Definition{{Name: "new_index", Kind: "index", SQL: `CREATE INDEX new_index ON "new table"(raw)`}}
	out := s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	requireDesignCommit(t, out)
	if _, err = s.db.Exec("CREATE TEMP TRIGGER local_trigger AFTER INSERT ON t BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	_, err = s.SchemaDesign().Snapshot(context.Background(), contract.SchemaDesignTarget{Object: s.Namespace("main").Object("table", "t"), Generation: s.Generation()})
	if err == nil || !strings.Contains(err.Error(), "temporary triggers") {
		t.Fatal(err)
	}
}

func TestSQLiteIndexAndTriggerChangesDoNotRebuildDataAndRollbackTogether(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(1,'same'),(2,'same'); CREATE INDEX ix ON t(value); CREATE TABLE log(value TEXT)")
	original := designSnapshot(t, s, "main", "t")
	desired := original.Schema.Table.Clone()
	desired.Definitions[0].SQL = "CREATE UNIQUE INDEX ix ON t(value)"
	desired.Definitions = append(desired.Definitions, schemaddl.Definition{Name: "tr", Kind: "trigger", SQL: "CREATE TRIGGER tr AFTER UPDATE ON t BEGIN INSERT INTO log VALUES(new.value); END"})
	out := s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "rolled_back" || out.Error == nil {
		t.Fatalf("index conflict: %+v", out)
	}
	after := designSnapshot(t, s, "main", "t")
	if after.Schema.Revision != original.Schema.Revision {
		t.Fatal("rolled back definitions changed revision")
	}
	var rootBefore, rootAfter int64
	if err := s.db.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name='t'").Scan(&rootBefore); err != nil {
		t.Fatal(err)
	}
	desired.Definitions[0].SQL = "CREATE INDEX ix ON t(value DESC)"
	out = s.SchemaDesign().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	requireDesignCommit(t, out)
	if err := s.db.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name='t'").Scan(&rootAfter); err != nil || rootAfter != rootBefore || out.Rows != 0 {
		t.Fatalf("definitions rebuilt table: %d %d %v", rootBefore, rootAfter, err)
	}
	if _, err := s.db.Exec("UPDATE t SET value='changed' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := s.db.QueryRow("SELECT value FROM log").Scan(&value); err != nil || value != "changed" {
		t.Fatalf("new trigger: %s %v", value, err)
	}
	noSQLiteShadow(t, s)
}
