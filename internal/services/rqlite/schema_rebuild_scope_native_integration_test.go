package rqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestNativeRQLiteSchemaRebuildCapturesInboundClosureWithoutTouchingUnrelatedRows(t *testing.T) {
	for _, fk := range []bool{false, true} {
		for _, action := range []string{"CASCADE", "SET NULL", "SET DEFAULT", "RESTRICT"} {
			label := fmt.Sprintf("fk%t-%s", fk, strings.ToLower(strings.ReplaceAll(action, " ", "-")))
			t.Run(label, func(t *testing.T) {
				p, _ := nativeFixtureConfigured(t, "", "", fk)
				s := openNative(t, p, "original-fixture-password")
				l := lease(t, s, 741)
				setup := "CREATE TABLE external(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO external VALUES(99,X'00FE');CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES external(id),v BLOB);INSERT INTO target VALUES(42,99,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE \"child Ω\"(id INTEGER PRIMARY KEY,parent INTEGER DEFAULT 42 REFERENCES target(id) ON DELETE " + action + ",v BLOB);INSERT INTO \"child Ω\" VALUES(7,42,X'80FF');CREATE TABLE grandchild(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES \"child Ω\"(id) ON DELETE CASCADE,v BLOB);INSERT INTO grandchild VALUES(9,7,X'0001');CREATE TABLE unrelated(rowid TEXT,oid TEXT,_rowid_ TEXT,v BLOB,parent INTEGER REFERENCES absent_parent(id));INSERT INTO unrelated VALUES('r','o','_r',X'610080FF',99);CREATE TABLE audit(v BLOB);CREATE TRIGGER target_update AFTER UPDATE ON target BEGIN INSERT INTO audit VALUES(new.v);END;CREATE VIEW vv AS SELECT * FROM target;CREATE TRIGGER vv_update INSTEAD OF UPDATE OF v ON vv BEGIN UPDATE target SET v=new.v WHERE id=old.id;END"
				rebuildBaselineSetup(t, s, fk, setup, "schema-rebuild-scope-"+label)
				d := reserveSchemaUnit(t, s, "original-scope-"+label, "target")
				original := nativeSchemaWriteSnapshot(t, d)
				desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "scope"})
				queries := []string{"SELECT id,parent,hex(v) FROM target", "SELECT id,parent,hex(v) FROM \"child Ω\"", "SELECT id,parent,hex(v) FROM grandchild", "SELECT id,hex(v) FROM external", "SELECT rowid,oid,_rowid_,hex(v),parent FROM unrelated", "SELECT hex(v) FROM audit", "SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence ORDER BY rowid", "SELECT hex(\"table\"),rowid,hex(parent),fkid FROM pragma_foreign_key_check ORDER BY 1,2,3,4"}
				read := func() [][][]contract.Cell {
					var frames [][][]contract.Cell
					for _, query := range queries {
						frames = append(frames, nativeExecute(t, l, query).Sets[0].Rows)
					}
					return frames
				}
				before := read()
				writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-scope-"+label+"-before")
				out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
				actual := requireSchemaCommit(t, "schema-rebuild-scope-"+label, out)
				after := read()
				writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-scope-"+label+"-after")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || schemaddl.VerifySQLiteRebuildCatalog(original.Schema, desired, actual.Schema) != nil {
					t.Fatal("selective capture changed original rows, FK baseline, programs, roots or writer")
				}
				var captured []string
				for _, sql := range out.Receipt.Statements {
					for _, name := range []string{"target", "child Ω", "grandchild"} {
						if strings.Contains(sql, "FROM main."+schemaddl.Quote(name)) && strings.HasPrefix(sql, "INSERT INTO temp.") && strings.Contains(sql, "_rows_") {
							captured = append(captured, name)
						}
					}
					for _, name := range []string{"external", "unrelated", "audit"} {
						if strings.Contains(sql, "main."+schemaddl.Quote(name)) {
							t.Fatal("original unrelated physical table was read, compiled or written", name, sql)
						}
					}
				}
				if len(captured) != 3 {
					t.Fatal("complete original inbound closure was not captured", captured)
				}
				proof, err := json.Marshal(struct {
					Before, After             [][][]contract.Cell
					WriterBefore, WriterAfter []contract.Cell
					Captured                  []string
					Outcome                   contract.SchemaDesignOutcome
				}{before, after, writerBefore, writerAfter, captured, out})
				if err != nil {
					t.Fatal(err)
				}
				retainNativeFileReceipt(t, "schema-rebuild-scope-"+label+"-verification", string(proof))
				nativeExecute(t, l, "UPDATE vv SET v=X'00AA' WHERE id=42")
				if nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("original view/table programs were not restored after scoped capture")
				}
			})
		}
	}
}

func TestNativeRQLiteSchemaRebuildRetainsLargeUnrelatedInaccessibleRowIDTable(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 742)
	rebuildBaselineSetup(t, s, true, "CREATE TABLE target(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO target VALUES(42,X'00FF');CREATE TABLE unrelated(rowid INTEGER UNIQUE,oid INTEGER,_rowid_ INTEGER,v BLOB,parent INTEGER REFERENCES absent_parent(id));WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100001) INSERT INTO unrelated SELECT x,-x,x+9007199254740993,X'00FE',99 FROM n", "schema-rebuild-scope-large")
	d := reserveSchemaUnit(t, s, "original-scope-large", "target")
	original := nativeSchemaWriteSnapshot(t, d)
	desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "scope-large"})
	query := "SELECT count(*),sum(rowid),min(rowid),max(rowid),sum(oid=-rowid),sum(_rowid_=rowid+9007199254740993),sum(typeof(v)='blob' AND hex(v)='00FE'),sum(parent=99) FROM unrelated"
	before := nativeExecute(t, l, query).Sets[0].Rows
	writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-scope-large-before")
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	actual := requireSchemaCommit(t, "schema-rebuild-scope-large", out)
	after := nativeExecute(t, l, query).Sets[0].Rows
	writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-scope-large-after")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || before[0][0].Text != "100001" || schemaddl.VerifySQLiteRebuildCatalog(original.Schema, desired, actual.Schema) != nil {
		t.Fatal("large unrelated physical rows, original catalog or writer changed")
	}
	for _, sql := range out.Receipt.Statements {
		if strings.Contains(sql, "main.\"unrelated\"") {
			t.Fatal("large unrelated inaccessible physical table entered reconstruction", sql)
		}
	}
	proof, err := json.Marshal(struct {
		Before, After             [][]contract.Cell
		WriterBefore, WriterAfter []contract.Cell
		Outcome                   contract.SchemaDesignOutcome
	}{before, after, writerBefore, writerAfter, out})
	if err != nil {
		t.Fatal(err)
	}
	retainNativeFileReceipt(t, "schema-rebuild-scope-large-verification", string(proof))
}
