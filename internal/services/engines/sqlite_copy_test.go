package engines

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	sqlitedriver "modernc.org/sqlite"
)

func copyRequest(t *testing.T, s *Session, schema, source, destination string, data bool) contract.SchemaCopyRequest {
	t.Helper()
	return contract.SchemaCopyRequest{Original: designSnapshot(t, s, schema, source), Destination: destination, WithData: data, RebindSelfReferences: true, PreserveRowID: true}
}
func noSQLiteCopy(t *testing.T, s *Session, name string) {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name=? OR tbl_name=?", name, name).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed copy left objects: %d %v", n, err)
	}
}

func TestSQLiteCopyFullSchemaTypedRowsSelfReferencesAndSequence(t *testing.T) {
	s := sqliteDesignFixture(t, `
CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES items(id),label TEXT COLLATE NOCASE,raw BLOB,n INTEGER,amount TEXT,gen TEXT GENERATED ALWAYS AS (label || ':' || n) STORED,CHECK(n>=0));
CREATE UNIQUE INDEX items_label ON items(label COLLATE BINARY DESC) WHERE label IS NOT NULL;
CREATE TABLE audit(value TEXT);
CREATE TRIGGER items_insert AFTER INSERT ON items BEGIN INSERT INTO audit VALUES('source body'); END;
INSERT INTO items VALUES(7,7,'Label',x'00ff',9223372036854775806,'12345678901234567890.123456789');
INSERT INTO items VALUES(8,7,NULL,NULL,0,NULL);
INSERT INTO items VALUES(100,7,'deleted high water',x'01',1,'0');DELETE FROM items WHERE id=100;DELETE FROM audit;
`)
	payload := bytes.Repeat([]byte{0, 255, 128, 17}, (1<<20)/4+1)
	if _, err := s.db.Exec("INSERT INTO items(id,parent,label,raw,n,amount) VALUES(9,7,'large',?,1,'0.000000000000000000001');DELETE FROM audit", payload); err != nil {
		t.Fatal(err)
	}
	r := copyRequest(t, s, "main", "items", "copied", true)
	out := s.SchemaDesign().Copy(context.Background(), r)
	requireDesignCommit(t, out)
	if out.Rows != 3 || out.Actual.Target.Object.Name != "copied" || len(out.Actual.Schema.Table.Definitions) != 2 {
		t.Fatalf("incomplete copy: %+v", out)
	}
	var raw []byte
	var amount, generated string
	var n int64
	if err := s.db.QueryRow("SELECT raw,n,amount,gen FROM copied WHERE id=7").Scan(&raw, &n, &amount, &generated); err != nil || !bytes.Equal(raw, []byte{0, 255}) || n != 9223372036854775806 || amount != "12345678901234567890.123456789" || generated != "Label:9223372036854775806" {
		t.Fatalf("typed copy: %x %d %s %s %v", raw, n, amount, generated, err)
	}
	if err := s.db.QueryRow("SELECT raw FROM copied WHERE id=9").Scan(&raw); err != nil || !bytes.Equal(raw, payload) {
		t.Fatal("large bytes changed", err)
	}
	var nulls, count int
	if err := s.db.QueryRow("SELECT label IS NULL AND raw IS NULL AND amount IS NULL AND gen IS NULL FROM copied WHERE id=8").Scan(&nulls); err != nil || nulls != 1 {
		t.Fatal("NULLs changed", err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil || count != 0 {
		t.Fatalf("copied insertion fired a trigger: %d %v", count, err)
	}
	var seq int64
	if err := s.db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='copied'").Scan(&seq); err != nil || seq != 100 {
		t.Fatalf("sequence high water changed: %d %v", seq, err)
	}
	if _, err := s.db.Exec("INSERT INTO copied(parent,label,n) VALUES(7,'next',1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT id FROM copied WHERE label='next'").Scan(&seq); err != nil || seq != 101 {
		t.Fatalf("copied next ID: %d %v", seq, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil || count != 1 {
		t.Fatal("copied trigger not active", err)
	}
	rows, err := s.db.Query("PRAGMA foreign_key_list(copied)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("self FK missing")
	}
	var id, part int
	var parent, from, to, update, delete, match string
	if err = rows.Scan(&id, &part, &parent, &from, &to, &update, &delete, &match); err != nil || parent != "copied" {
		t.Fatal("self FK not rebound", parent, err)
	}
}

func TestSQLiteCopyKeepsColumnsDistinctUnderSQLiteASCIICaseRules(t *testing.T) {
	s := sqliteDesignFixture(t, `CREATE TABLE t(id INTEGER PRIMARY KEY,"K" TEXT,"K" TEXT);INSERT INTO t VALUES(1,'ASCII','Unicode')`)
	out := s.SchemaDesign().Copy(context.Background(), copyRequest(t, s, "main", "t", "copied", true))
	requireDesignCommit(t, out)
	var ascii, unicode string
	if err := s.db.QueryRow(`SELECT "K","K" FROM copied`).Scan(&ascii, &unicode); err != nil || ascii != "ASCII" || unicode != "Unicode" {
		t.Fatal("Unicode column conflated", ascii, unicode, err)
	}
}

func TestSQLiteCopyStructureOnlyPreservesStrictOptionsAndResetsSequence(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT DEFAULT '001') STRICT; CREATE INDEX idx ON t(value); INSERT INTO t VALUES(99,'source')")
	r := copyRequest(t, s, "main", "t", "empty_copy", false)
	out := s.SchemaDesign().Copy(context.Background(), r)
	requireDesignCommit(t, out)
	if out.Rows != 0 || out.Actual.Schema.Table.Options != "STRICT" {
		t.Fatalf("structure copy options: %+v", out)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM empty_copy").Scan(&count); err != nil || count != 0 {
		t.Fatal("structure copy inserted rows", err)
	}
	if _, err := s.db.Exec("INSERT INTO empty_copy DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
	var id int64
	var value string
	if err := s.db.QueryRow("SELECT id,value FROM empty_copy").Scan(&id, &value); err != nil || id != 1 || value != "001" {
		t.Fatalf("structure sequence/default: %d %s %v", id, value, err)
	}
	if err := s.db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='t'").Scan(&id); err != nil || id != 99 {
		t.Fatal("source sequence changed", err)
	}
}

func TestSQLiteCopyPreservesSequenceAbsenceAndExactManualHighWater(t *testing.T) {
	for _, phase := range []string{"absent", "zero", "negative", "below maximum", "invalid", "duplicate"} {
		t.Run(phase, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT)")
			switch phase {
			case "zero":
				_, _ = s.db.Exec("INSERT INTO sqlite_sequence VALUES('t',0)")
			case "negative":
				_, _ = s.db.Exec("INSERT INTO sqlite_sequence VALUES('t',-7)")
			case "below maximum":
				_, _ = s.db.Exec("INSERT INTO t VALUES(20,'saved');UPDATE sqlite_sequence SET seq=3 WHERE name='t'")
			case "invalid":
				_, _ = s.db.Exec("INSERT INTO sqlite_sequence VALUES('t','not integer')")
			case "duplicate":
				_, _ = s.db.Exec("INSERT INTO sqlite_sequence VALUES('t',1),('t',2)")
			}
			out := s.SchemaDesign().Copy(context.Background(), copyRequest(t, s, "main", "t", "copied", true))
			if phase == "invalid" || phase == "duplicate" {
				if out.Error == nil || out.State != "rolled_back" {
					t.Fatalf("invalid sequence accepted: %+v", out)
				}
				noSQLiteCopy(t, s, "copied")
				return
			}
			requireDesignCommit(t, out)
			before, err := sqliteReadCopySequenceOnDB(t, s, "t")
			if err != nil {
				t.Fatal(err)
			}
			after, err := sqliteReadCopySequenceOnDB(t, s, "copied")
			if err != nil || before != after {
				t.Fatalf("sequence not exact: %+v %+v %v", before, after, err)
			}
		})
	}
}
func sqliteReadCopySequenceOnDB(t *testing.T, s *Session, table string) (sqliteCopySequence, error) {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		return sqliteCopySequence{}, err
	}
	defer c.Close()
	return sqliteReadCopySequence(context.Background(), c, "main", table)
}

func TestSQLiteCopyPreservesHiddenRowidsAndWithoutRowidKeys(t *testing.T) {
	for _, item := range []struct{ name, ddl string }{
		{"ordinary", `CREATE TABLE t(value TEXT); INSERT INTO t(rowid,value) VALUES(-9,'duplicate'),(99,'duplicate')`},
		{"one shadow", `CREATE TABLE t(rowid TEXT,value TEXT); INSERT INTO t(_rowid_,rowid,value) VALUES(-9,'named rowid','duplicate'),(99,'named rowid','duplicate')`},
		{"all shadow true key", `CREATE TABLE t(rowid TEXT,_rowid_ TEXT,oid TEXT,id INTEGER PRIMARY KEY); INSERT INTO t VALUES('r','_r','o',99)`},
		{"without rowid", `CREATE TABLE t(a TEXT COLLATE NOCASE,b BLOB,value TEXT,PRIMARY KEY(a,b)) WITHOUT ROWID; INSERT INTO t VALUES('A',x'00ff','saved')`},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := sqliteDesignFixture(t, item.ddl)
			r := copyRequest(t, s, "main", "t", "copied", true)
			requireDesignCommit(t, s.SchemaDesign().Copy(context.Background(), r))
			c, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			cols, err := sqliteDesignColumns(context.Background(), c, "main", "t")
			if err != nil {
				t.Fatal(err)
			}
			var expressions []string
			if !schemaddl.SQLiteWithoutRowID(r.Original.Schema.Table) {
				identity, err := sqliteCopyRowIdentity(context.Background(), c, "main", "t", cols)
				if err != nil {
					t.Fatal(err)
				}
				expressions = append(expressions, quote(identity))
			}
			for _, col := range cols {
				expressions = append(expressions, quote(col.Name))
			}
			before, err := sqliteValueDigest(context.Background(), c, "main", "t", expressions)
			if err != nil {
				t.Fatal(err)
			}
			after, err := sqliteValueDigest(context.Background(), c, "main", "copied", expressions)
			if err != nil || before != after {
				t.Fatal("identity/values changed", err)
			}
		})
	}
}

func TestSQLiteCopyAllShadowedRowidsRequireExplicitChoice(t *testing.T) {
	for _, ddl := range []string{`CREATE TABLE t(rowid TEXT,_rowid_ TEXT,oid TEXT,value TEXT); INSERT INTO t VALUES('r','_r','o','saved')`, `CREATE TABLE t(rowid TEXT,_rowid_ TEXT,oid TEXT,id INTEGER PRIMARY KEY DESC); INSERT INTO t VALUES('r','_r','o',7)`} {
		s := sqliteDesignFixture(t, ddl)
		r := copyRequest(t, s, "main", "t", "copied", true)
		out := s.SchemaDesign().Copy(context.Background(), r)
		if out.State != "rolled_back" || out.Error == nil || !strings.Contains(out.Error.Error(), "inaccessible") {
			t.Fatalf("silently reassigned rowids: %+v", out)
		}
		noSQLiteCopy(t, s, "copied")
		r.PreserveRowID = false
		requireDesignCommit(t, s.SchemaDesign().Copy(context.Background(), r))
	}
}

func TestSQLiteCopyConflictsRollbackWithoutOverwritingAnyObject(t *testing.T) {
	for _, kind := range []string{"destination", "definition", "namespace", "replacement", "forged", "profile", "generation"} {
		t.Run(kind, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT);CREATE INDEX idx ON t(value);INSERT INTO t VALUES(1,'saved')")
			if kind == "destination" {
				_, _ = s.db.Exec("CREATE TABLE copied(id);INSERT INTO copied VALUES(99)")
			}
			if kind == "definition" {
				_, _ = s.db.Exec("CREATE TABLE unrelated(id);CREATE INDEX copied_idx ON unrelated(id)")
			}
			r := copyRequest(t, s, "main", "t", "copied", true)
			switch kind {
			case "namespace":
				_, _ = s.db.Exec("CREATE TABLE unrelated(id)")
			case "replacement":
				_, _ = s.db.Exec("DROP TABLE t;CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT);CREATE INDEX idx ON t(value);INSERT INTO t VALUES(2,'replacement')")
			case "forged":
				r.Original.Schema.Table.Clauses[1].SQL = "BLOB"
			case "profile":
				r.Original.Target.Object.ProfileID++
			case "generation":
				r.Original.Target.Generation++
			}
			out := s.SchemaDesign().Copy(context.Background(), r)
			if out.Error == nil || out.Verified || out.State == "committed" {
				t.Fatalf("unsafe copy: %+v", out)
			}
			if kind == "namespace" || kind == "replacement" || kind == "forged" {
				if !errors.Is(out.Error, schemaddl.ErrConflict) {
					t.Fatal(out.Error)
				}
			}
			if kind == "destination" {
				var id int
				if err := s.db.QueryRow("SELECT id FROM copied").Scan(&id); err != nil || id != 99 {
					t.Fatal("existing destination changed", err)
				}
			} else {
				noSQLiteCopy(t, s, "copied")
			}
		})
	}
}

func TestSQLiteCopyAttachedNamespaceAndReadOnlyIsolation(t *testing.T) {
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
			r := copyRequest(t, s, `a"ux`, "shared", "copied", true)
			ops := s.SchemaDesign()
			if ops.CanCopy(r.Original.Target) == readOnly {
				t.Fatal("wrong copy availability")
			}
			out := ops.Copy(context.Background(), r)
			if readOnly {
				if out.Error == nil || out.State != "not_executed" {
					t.Fatalf("read-only copy: %+v", out)
				}
			} else {
				requireDesignCommit(t, out)
				var value string
				if err := s.db.QueryRow(`SELECT value FROM "a""ux".copied`).Scan(&value); err != nil || value != "attached" {
					t.Fatal("copy used another source namespace", err)
				}
			}
			var value string
			if err := s.db.QueryRow("SELECT value FROM main.shared").Scan(&value); err != nil || value != "main" {
				t.Fatal("source main changed", err)
			}
			noSQLiteCopy(t, s, "copied")
		})
	}
}

var copyCancelFunctionID atomic.Uint64

func TestSQLiteCopyCancellationDuringRealInsertionRollsBackDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var armed, called atomic.Bool
	name := fmt.Sprintf("nyadb_copy_cancel_%d", copyCancelFunctionID.Add(1))
	if err := sqlitedriver.RegisterDeterministicScalarFunction(name, 1, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		if armed.Load() {
			called.Store(true)
			cancel()
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	s := sqliteDesignFixture(t, "SELECT 1")
	// The production connector owns a private driver. Use the registered test
	// driver only for this deterministic insertion interrupt, retaining the
	// real SQLite service, schema gate, transaction and receipt handling.
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(s.catalog)))
	if err != nil {
		t.Fatal(err)
	}
	s.db = db
	s.db.SetMaxOpenConns(1)
	if _, err = s.db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT,generated TEXT GENERATED ALWAYS AS (" + name + "(value)) STORED);INSERT INTO t(id,value) VALUES(1,'saved'),(2,NULL)"); err != nil {
		t.Fatal(err)
	}
	r := copyRequest(t, s, "main", "t", "copied", true)
	armed.Store(true)
	out := s.SchemaDesign().Copy(ctx, r)
	armed.Store(false)
	if !called.Load() || ctx.Err() == nil || out.Error == nil || out.Verified || (out.State != "rolled_back" && out.State != "uncertain") {
		t.Fatalf("actual insertion cancellation: %+v", out)
	}
	// SQLite may roll back the transaction itself on interrupt. If the explicit
	// rollback acknowledgement then fails, the service conservatively reports
	// uncertainty and discards that physical connection; it must not claim a
	// verified commit. Inspect storage before an explicit new request.
	if out.State == "uncertain" && !errors.Is(out.Error, ErrUncertain) {
		t.Fatal(out.Error)
	}
	noSQLiteCopy(t, s, "copied")
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM t WHERE id=1 AND value='saved' AND generated='saved' OR id=2 AND value IS NULL AND generated IS NULL").Scan(&count); err != nil || count != 2 {
		t.Fatal("cancel changed original rows", count, err)
	}
	requireDesignCommit(t, s.SchemaDesign().Copy(context.Background(), copyRequest(t, s, "main", "t", "copied", true)))
}

func TestSQLiteCopyCancelledRealLockWaitLeavesNoDestination(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT);INSERT INTO t VALUES(1,'saved')")
	r := copyRequest(t, s, "main", "t", "copied", true)
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
	out := s.SchemaDesign().Copy(ctx, r)
	if out.Error == nil || out.Verified || out.State == "committed" || ctx.Err() == nil {
		t.Fatalf("lock wait not cancelled: %+v", out)
	}
	if _, err = blocker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	noSQLiteCopy(t, s, "copied")
	requireDesignCommit(t, s.SchemaDesign().Copy(context.Background(), r))
}
