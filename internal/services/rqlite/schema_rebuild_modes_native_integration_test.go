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

func rebuildModeWriterState(t *testing.T, s *Session, label string) []contract.Cell {
	t.Helper()
	r, err := s.request(context.Background(), "/db/execute", []any{
		"CREATE TEMP TABLE __nyadb_modes_observer(wfk INTEGER,wdefer INTEGER,wlegacy INTEGER,wobjects INTEGER)",
		"INSERT INTO __nyadb_modes_observer SELECT (SELECT foreign_keys FROM pragma_foreign_keys),(SELECT defer_foreign_keys FROM pragma_defer_foreign_keys),(SELECT legacy_alter_table FROM pragma_legacy_alter_table),(SELECT count(*) FROM temp.sqlite_schema WHERE name<>'__nyadb_modes_observer')",
		"DELETE FROM __nyadb_modes_observer RETURNING wfk,wdefer,wlegacy,wobjects",
		"DROP TABLE temp.__nyadb_modes_observer",
	}, true, true)
	retainNativeFileReceipt(t, label+"-writer-state", r.Raw)
	rows, decodeErr := decodedRows(r, 2, 4)
	if err != nil || decodeErr != nil || len(rows) != 1 {
		t.Fatal("complete original writer state missing", err, decodeErr)
	}
	return rows[0]
}

func TestNativeRQLiteSchemaRebuildWriterModesAndImmediateActionsRetainOriginalFrames(t *testing.T) {
	for _, fk := range []bool{false, true} {
		for _, legacy := range []int{0, 1} {
			for _, action := range []string{"SET NULL", "SET DEFAULT", "CASCADE"} {
				label := fmt.Sprintf("fk%t-legacy%d-%s", fk, legacy, strings.ToLower(strings.ReplaceAll(action, " ", "-")))
				t.Run(label, func(t *testing.T) {
					p, _ := nativeFixtureConfigured(t, "", "", fk)
					s := openNative(t, p, "original-fixture-password")
					l := lease(t, s, 731)
					setup := []any{
						fmt.Sprintf("PRAGMA legacy_alter_table=%d", legacy),
						"CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER NOT NULL DEFAULT 99 REFERENCES child(id) ON DELETE " + action + ",v ANY,g TEXT GENERATED ALWAYS AS(hex(parent)) STORED,CHECK(parent IN(1,2))) STRICT",
						"CREATE TABLE child(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER NOT NULL DEFAULT 99 REFERENCES target(id) ON DELETE " + action + ",v BLOB,CHECK(parent IN(1,2)))",
						"PRAGMA defer_foreign_keys=ON",
						"INSERT INTO target(id,parent,v) VALUES(1,2,CAST(X'610080FF' AS TEXT))",
						"INSERT INTO child(id,parent,v) VALUES(2,1,X'00FE')",
						"UPDATE sqlite_sequence SET seq=5000 WHERE name='target'",
						"UPDATE sqlite_sequence SET seq=7000 WHERE name='child'",
						"CREATE TABLE audit(v BLOB)",
						"CREATE TRIGGER target_update AFTER UPDATE ON target BEGIN INSERT INTO audit VALUES(new.v); END",
						"CREATE VIEW vv AS SELECT id,v FROM target",
						"CREATE TRIGGER vv_update INSTEAD OF UPDATE OF v ON vv BEGIN UPDATE target SET v=new.v WHERE id=old.id; END",
					}
					created, err := s.request(context.Background(), "/db/execute", setup, true, true)
					retainNativeFileReceipt(t, "schema-rebuild-modes-"+label+"-setup", created.Raw)
					if err != nil {
						t.Fatal(err)
					}
					d := reserveSchemaUnit(t, s, "original-writer-modes-"+label, "target")
					original := nativeSchemaWriteSnapshot(t, d)
					desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "modes"})
					tables := rebuildProtocolTables(t, s, original)
					before := rebuildProtocolFingerprints(t, l, tables)
					writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-modes-"+label+"-before")
					plan, err := d.Operations().Plan(contract.SchemaDesignRequest{Original: original, Desired: desired})
					if err != nil || plan.Mode != "rebuild" || d.sequence != 0 {
						t.Fatal("writer mode preview was not pure", err)
					}
					out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
					actual := requireSchemaCommit(t, "schema-rebuild-modes-"+label, out)
					after := rebuildProtocolFingerprints(t, l, tables)
					writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-modes-"+label+"-after")
					if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || writerBefore[0].Text != fmt.Sprint(map[bool]int{false: 0, true: 1}[fk]) || writerBefore[1].Text != "0" || writerBefore[2].Text != fmt.Sprint(legacy) || writerBefore[3].Text != "0" {
						t.Fatal("original data, sequence or writer settings changed")
					}
					if schemaddl.VerifySQLiteRebuildCatalog(original.Schema, desired, actual.Schema) != nil {
						t.Fatal("original unselected programs or roots changed")
					}
					proof, _ := json.Marshal(struct {
						Before, After             map[string][][]contract.Cell
						WriterBefore, WriterAfter []contract.Cell
						Outcome                   contract.SchemaDesignOutcome
					}{before, after, writerBefore, writerAfter, out})
					retainNativeFileReceipt(t, "schema-rebuild-modes-"+label+"-verification", string(proof))
					if fk && action != "CASCADE" {
						rejected, err := l.Execute(context.Background(), "main", "DELETE FROM child WHERE id=2")
						retainNativeFileReceipt(t, "schema-rebuild-modes-"+label+"-normal-delete-rejection", rejected.NativeJSON)
						if err == nil || !reflect.DeepEqual(before, rebuildProtocolFingerprints(t, l, tables)) {
							t.Fatal("private maintenance triggers leaked into normal FK enforcement", err)
						}
					}
					nativeExecute(t, l, "UPDATE vv SET v=X'00AA' WHERE id=1")
					if nativeExecute(t, l, "SELECT hex(v) FROM target;SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "00AA" || nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "1" {
						t.Fatal("original view and table UPDATE programs were not restored")
					}
				})
			}
		}
	}
}

func TestNativeRQLiteSchemaRebuildDisabledForeignKeysRejectInvalidOriginalAndFinalGraphs(t *testing.T) {
	for _, mode := range []string{"original-invalid", "final-invalid"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := nativeFixtureConfigured(t, "", "", false)
			s := openNative(t, p, "original-fixture-password")
			l := lease(t, s, 732)
			nativeExecute(t, l, "CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO target VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id));INSERT INTO child VALUES(7,42);CREATE VIEW vv AS SELECT * FROM target")
			if mode == "original-invalid" {
				nativeExecute(t, l, "UPDATE child SET parent=99")
			}
			d := reserveSchemaUnit(t, s, "original-fk-off-graph-"+mode, "target")
			original := nativeSchemaWriteSnapshot(t, d)
			desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "fk-off-graph"})
			if mode == "final-invalid" {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:fk", Kind: "constraint", SQL: "FOREIGN KEY(v) REFERENCES child(id)"})
			}
			tables := rebuildProtocolTables(t, s, original)
			before := rebuildProtocolFingerprints(t, l, tables)
			writerBefore := rebuildModeWriterState(t, s, "schema-rebuild-fk-off-graph-"+mode+"-before")
			out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
			retainSchemaWriteOutcome(t, "schema-rebuild-fk-off-graph-"+mode, out)
			after := rebuildProtocolFingerprints(t, l, tables)
			writerAfter := rebuildModeWriterState(t, s, "schema-rebuild-fk-off-graph-"+mode+"-after")
			actual := nativeSchemaWriteSnapshot(t, d)
			if out.State != "rolled_back" || out.Verified || out.Error == nil || out.Receipt == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || !reflect.DeepEqual(original, actual) {
				t.Fatal("disabled FK enforcement accepted an invalid graph or changed original state", out.State, out.Error)
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
			retainNativeFileReceipt(t, "schema-rebuild-fk-off-graph-"+mode+"-verification", string(proof))
		})
	}
}
