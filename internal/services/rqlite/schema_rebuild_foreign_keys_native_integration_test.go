package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func rebuildBaselineSetup(t *testing.T, s *Session, fk bool, setup string, label string) {
	t.Helper()
	sql := []any{"PRAGMA foreign_keys=OFF"}
	parts, splitErr := splitSQL(setup)
	if splitErr != nil {
		t.Fatal(splitErr)
	}
	for _, statement := range parts {
		sql = append(sql, statement)
	}
	sql = append(sql, fmt.Sprintf("PRAGMA foreign_keys=%d", map[bool]int{false: 0, true: 1}[fk]))
	r, err := s.request(context.Background(), "/db/execute", sql, false, true)
	retainNativeFileReceipt(t, label+"-setup", r.Raw)
	if err != nil || len(r.Results) != len(sql) {
		t.Fatal("complete original baseline fixture setup failed", err)
	}
}

func TestNativeRQLiteSchemaRebuildRetainsOriginalForeignKeyViolationMultisets(t *testing.T) {
	common := "CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO target VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE audit(v BLOB);CREATE VIEW vv AS SELECT * FROM target;CREATE TRIGGER vv_update INSTEAD OF UPDATE OF v ON vv BEGIN UPDATE target SET v=new.v WHERE id=old.id;END;CREATE TRIGGER target_update AFTER UPDATE ON target BEGIN INSERT INTO audit VALUES(new.v);END"
	for _, fk := range []bool{false, true} {
		for _, c := range []struct {
			name, extra string
			count       int
			remove      bool
		}{
			{"unselected", "CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id));INSERT INTO child VALUES(-9,99),(9007199254740993,100)", 2, false},
			{"null-multiplicity", "CREATE TABLE parent(a INTEGER,b TEXT,PRIMARY KEY(a,b));CREATE TABLE child(k INTEGER PRIMARY KEY,a INTEGER,b TEXT,FOREIGN KEY(a,b) REFERENCES parent(a,b)) WITHOUT ROWID;INSERT INTO child VALUES(1,99,'x'),(2,99,'x')", 2, false},
			{"missing-parent", "CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES absent_parent(id));INSERT INTO child VALUES(1,99)", 1, false},
			{"quoted-names", "CREATE TABLE \"parent odd' Ω\"(id INTEGER PRIMARY KEY);CREATE TABLE \"child odd' Ω\"(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES \"parent odd' Ω\"(id));INSERT INTO \"child odd' Ω\" VALUES(0,99)", 1, false},
			{"selected", "CREATE TABLE parent(id INTEGER PRIMARY KEY);DROP TABLE target;CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES parent(id),v BLOB);INSERT INTO target VALUES(42,99,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TRIGGER target_update AFTER UPDATE ON target BEGIN INSERT INTO audit VALUES(new.v);END", 1, false},
			{"remove", "CREATE TABLE parent(id INTEGER PRIMARY KEY);DROP TABLE target;CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER,v BLOB,FOREIGN KEY(parent) REFERENCES parent(id));INSERT INTO target VALUES(42,99,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TRIGGER target_update AFTER UPDATE ON target BEGIN INSERT INTO audit VALUES(new.v);END", 1, true},
		} {
			label := fmt.Sprintf("fk%t-%s", fk, c.name)
			t.Run(label, func(t *testing.T) {
				p, _ := nativeFixtureConfigured(t, "", "", fk)
				s := openNative(t, p, "original-fixture-password")
				l := lease(t, s, 733)
				rebuildBaselineSetup(t, s, fk, common+";"+c.extra, "schema-rebuild-baseline-"+label)
				d := reserveSchemaUnit(t, s, "original-baseline-"+label, "target")
				original := nativeSchemaWriteSnapshot(t, d)
				desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "baseline"})
				if c.remove {
					clauses := desired.Clauses[:0]
					for _, clause := range desired.Clauses {
						if clause.Kind != "constraint" || !strings.HasPrefix(clause.SQL, "FOREIGN KEY") {
							clauses = append(clauses, clause)
						}
					}
					desired.Clauses = clauses
				}
				tables := rebuildProtocolTables(t, s, original)
				before := rebuildProtocolFingerprints(t, l, tables)
				violationsBefore := nativeExecute(t, l, "SELECT hex(\"table\"),rowid,hex(parent),fkid FROM pragma_foreign_key_check ORDER BY 1,2,3,4").Sets[0].Rows
				writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-baseline-"+label+"-before")
				plan, err := d.Operations().Plan(contract.SchemaDesignRequest{Original: original, Desired: desired})
				if err != nil || plan.Mode != "rebuild" || d.sequence != 0 || len(violationsBefore) != c.count {
					t.Fatal("original baseline preview or complete violations missing", err)
				}
				out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
				actual := requireSchemaCommit(t, "schema-rebuild-baseline-"+label, out)
				retainSchemaWriteOutcome(t, "schema-rebuild-baseline-"+label, out)
				after := rebuildProtocolFingerprints(t, l, tables)
				violationsAfter := nativeExecute(t, l, "SELECT hex(\"table\"),rowid,hex(parent),fkid FROM pragma_foreign_key_check ORDER BY 1,2,3,4").Sets[0].Rows
				writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-baseline-"+label+"-after")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || c.remove && len(violationsAfter) != 0 || !c.remove && !reflect.DeepEqual(violationsBefore, violationsAfter) || schemaddl.VerifySQLiteRebuildCatalog(original.Schema, desired, actual.Schema) != nil {
					t.Fatal("baseline reconstruction changed original values, programs, writer or violations")
				}
				proof, err := json.Marshal(struct {
					Before, After                     map[string][][]contract.Cell
					ViolationsBefore, ViolationsAfter [][]contract.Cell
					WriterBefore, WriterAfter         []contract.Cell
					Outcome                           contract.SchemaDesignOutcome
				}{before, after, violationsBefore, violationsAfter, writerBefore, writerAfter, out})
				if err != nil {
					t.Fatal(err)
				}
				retainNativeFileReceipt(t, "schema-rebuild-baseline-"+label+"-verification", string(proof))
				nativeExecute(t, l, "UPDATE vv SET v=X'00AA' WHERE id=42")
				if nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("original programs were not restored after baseline-aware reconstruction")
				}
			})
		}
	}
}

func TestNativeRQLiteSchemaRebuildRejectsAddedViolationsAgainstOriginalMultiset(t *testing.T) {
	for _, fk := range []bool{false, true} {
		for _, mode := range []string{"null-multiplicity", "foreign-key-id"} {
			label := fmt.Sprintf("fk%t-%s", fk, mode)
			t.Run(label, func(t *testing.T) {
				p, _ := nativeFixtureConfigured(t, "", "", fk)
				s := openNative(t, p, "original-fixture-password")
				l := lease(t, s, 734)
				setup := "CREATE TABLE target(k TEXT COLLATE NOCASE PRIMARY KEY,v BLOB);INSERT INTO target VALUES('A',X'00FF');CREATE TABLE child(k INTEGER PRIMARY KEY,parent TEXT REFERENCES target(k)) WITHOUT ROWID;INSERT INTO child VALUES(1,'x'),(2,'y'),(3,'a')"
				if mode == "foreign-key-id" {
					setup = "CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE target(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES parent(id),v BLOB);INSERT INTO target VALUES(42,99,X'00FF')"
				}
				rebuildBaselineSetup(t, s, fk, setup, "schema-rebuild-baseline-added-"+label)
				d := reserveSchemaUnit(t, s, "original-baseline-added-"+label, "target")
				original := nativeSchemaWriteSnapshot(t, d)
				desired := original.Schema.Table.Clone()
				if mode == "null-multiplicity" {
					desired.Clauses[0].SQL = "TEXT COLLATE BINARY PRIMARY KEY"
				} else {
					desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:self", Kind: "constraint", SQL: "FOREIGN KEY(id) REFERENCES target(id)"})
				}
				tables := rebuildProtocolTables(t, s, original)
				before := rebuildProtocolFingerprints(t, l, tables)
				writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-baseline-added-"+label+"-before")
				out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
				retainSchemaWriteOutcome(t, "schema-rebuild-baseline-added-"+label, out)
				after := rebuildProtocolFingerprints(t, l, tables)
				writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-baseline-added-"+label+"-after")
				actual := nativeSchemaWriteSnapshot(t, d)
				if out.State != "rolled_back" || out.Verified || out.Error == nil || out.Receipt == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || !reflect.DeepEqual(original, actual) {
					t.Fatal("baseline comparison accepted additional multiplicity or changed FK identity", out.State, out.Error)
				}
				proof, err := json.Marshal(struct {
					Before, After             map[string][][]contract.Cell
					WriterBefore, WriterAfter []contract.Cell
					Original, Actual          contract.SchemaDesignSnapshot
					Outcome                   contract.SchemaDesignOutcome
				}{before, after, writerBefore, writerAfter, original, actual, out})
				if err != nil {
					t.Fatal(err)
				}
				retainNativeFileReceipt(t, "schema-rebuild-baseline-added-"+label+"-verification", string(proof))
			})
		}
	}
}

func TestNativeRQLiteSchemaRebuildCompleteLargeForeignKeyBaselineReceipt(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", false)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 735)
	rebuildBaselineSetup(t, s, false, "CREATE TABLE target(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO target VALUES(42,X'00FF');CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id),v BLOB);WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100001) INSERT INTO child SELECT x,99,X'00FE' FROM n", "schema-rebuild-baseline-large")
	d := reserveSchemaUnit(t, s, "original-baseline-large", "target")
	original := nativeSchemaWriteSnapshot(t, d)
	desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "large-baseline"})
	query := "SELECT count(*),sum(id),min(id),max(id),sum(parent=99),sum(typeof(v)='blob' AND hex(v)='00FE') FROM child"
	before := nativeExecute(t, l, query).Sets[0].Rows
	writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-baseline-large-before")
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
	requireSchemaCommit(t, "schema-rebuild-baseline-large", out)
	after := nativeExecute(t, l, query).Sets[0].Rows
	writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-baseline-large-after")
	var raw receipt
	if err := json.Unmarshal([]byte(out.Receipt.NativeJSON), &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, frame := range raw.Results {
		if len(frame.Columns) == 6 && frame.Columns[0] == "fk_phase" {
			found = true
			if len(frame.Values) != 200004 {
				t.Fatal("large original baseline was truncated", len(frame.Values))
			}
		}
	}
	if !found || before[0][0].Text != "100001" || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) {
		t.Fatal("complete admitted large baseline or original typed rows were not preserved")
	}
	proof, err := json.Marshal(struct {
		Before, After             [][]contract.Cell
		WriterBefore, WriterAfter []contract.Cell
		Outcome                   contract.SchemaDesignOutcome
	}{before, after, writerBefore, writerAfter, out})
	if err != nil {
		t.Fatal(err)
	}
	retainNativeFileReceipt(t, "schema-rebuild-baseline-large-verification", string(proof))
}

func TestNativeRQLiteSchemaRebuildForeignKeyBaselineReceiptFaultsBlockReplay(t *testing.T) {
	for _, mode := range []string{"fk-label", "fk-missing", "fk-null-rowid", "fk-valid-frame", "fk-duplicate", "fk-count", "fk-affected"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := nativeFixtureConfigured(t, "", "", false)
			s := openNative(t, p, "original-fixture-password")
			l := lease(t, s, 736)
			rebuildBaselineSetup(t, s, false, "CREATE TABLE target(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO target VALUES(42,X'00FF');CREATE TABLE child(k INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id)) WITHOUT ROWID;INSERT INTO child VALUES(1,99),(2,99)", "schema-rebuild-baseline-fault-"+mode)
			d := reserveSchemaUnit(t, s, "original-baseline-fault-"+mode, "target")
			original := nativeSchemaWriteSnapshot(t, d)
			tables := rebuildProtocolTables(t, s, original)
			before := rebuildProtocolFingerprints(t, l, tables)
			desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "baseline-fault"})
			r := contract.SchemaDesignRequest{Original: original, Desired: desired}
			plan, err := d.Operations().Plan(r)
			if err != nil {
				t.Fatal(err)
			}
			capture := -1
			for i, sql := range plan.SQL {
				if strings.Contains(sql, "(ok,fk_phase,fk_table_hex") && strings.Contains(sql, schemaTextLiteral("before")) {
					capture = i
					break
				}
			}
			if capture < 0 {
				t.Fatal("original capture receipt index missing")
			}
			base := s.client.Transport
			mutation := &rebuildReceiptMutation{base: base, mode: mode, fkCaptureIndex: capture}
			s.client.Transport = mutation
			out := d.Operations().Apply(context.Background(), r)
			s.client.Transport = base
			retainNativeFileReceipt(t, "schema-rebuild-baseline-fault-"+mode+"-transport-original", mutation.original)
			retainSchemaWriteOutcome(t, "schema-rebuild-baseline-fault-"+mode, out)
			if out.State != "committed" || out.Verified || out.Actual == nil || out.Receipt == nil || out.Receipt.NativeJSON != mutation.emitted || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
				t.Fatal("incomplete original baseline receipt was accepted or discarded", out.State, out.Error)
			}
			if !reflect.DeepEqual(before, rebuildProtocolFingerprints(t, l, tables)) {
				t.Fatal("acknowledgement fault changed original complete typed values")
			}
			saved := d.OriginalOutcome()
			_ = nativeSchemaWriteSnapshot(t, d)
			if next := d.Operations().Apply(context.Background(), r); next.State != "not_executed" || mutation.writes != 1 || !reflect.DeepEqual(saved, d.OriginalOutcome()) {
				t.Fatal("read erased incomplete baseline acknowledgement or replayed reconstruction")
			}
		})
	}
}
