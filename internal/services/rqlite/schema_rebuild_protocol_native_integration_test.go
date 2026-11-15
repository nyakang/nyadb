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

// This is native protocol evidence, not a published schema executor. In
// particular, PlanSQLiteNative must keep rejecting rebuild-only changes until
// original-document settlement, complete catalog verification and the public
// workflow have also been implemented and verified.
type rebuildProtocolTable struct {
	table              schemaddl.Table
	source, saved      string
	identity           string
	columns, savedCols []string
	parents            []string
}

func rebuildProtocolIdentity(t *testing.T, table schemaddl.Table) string {
	t.Helper()
	if schemaddl.SQLiteWithoutRowID(table) {
		return ""
	}
	if name := schemaddl.SQLiteRowIDColumn(table); name != "" {
		return name
	}
	var names []string
	for _, c := range table.Columns() {
		names = append(names, c.Name)
	}
	for _, name := range []string{"_rowid_", "rowid", "oid"} {
		if !copyColumnPresent(names, name) {
			return name
		}
	}
	t.Fatal("protocol cannot preserve an inaccessible original physical rowid")
	return ""
}

// Raw TEMP columns have no declared type or affinity. The queue keeps the
// 2000-column-plus-hidden-rowid case within SQLite's SELECT expression bound.
func rebuildProtocolTransfer(source, destination string, sourceColumns, destinationColumns []string, identity, stem string) []string {
	if len(sourceColumns) <= 2000 {
		return []string{"INSERT INTO " + destination + " (" + strings.Join(destinationColumns, ",") + ") SELECT " + strings.Join(sourceColumns, ",") + " FROM " + source}
	}
	queueName, triggerName := schemaddl.Quote(stem+"_queue"), schemaddl.Quote(stem+"_transfer")
	queue := "temp." + queueName
	values := make([]string, len(sourceColumns))
	for i, c := range sourceColumns {
		values[i] = "(SELECT captured_source." + c + " FROM " + source + " AS captured_source WHERE captured_source." + schemaddl.Quote(identity) + "=NEW.source_rowid)"
	}
	// INSERT targets in trigger bodies cannot be qualified. All TEMP names are
	// private, and the initial guard rejects pre-existing TEMP views/triggers.
	_, destinationName, _ := strings.Cut(destination, ".")
	return []string{
		"CREATE TEMP TABLE " + queue + "(source_rowid INTEGER PRIMARY KEY)",
		"CREATE TEMP TRIGGER " + triggerName + " AFTER INSERT ON " + queueName + " BEGIN INSERT INTO " + destinationName + " (" + strings.Join(destinationColumns, ",") + ") VALUES (" + strings.Join(values, ",") + "); END",
		"INSERT INTO " + queue + "(source_rowid) SELECT " + schemaddl.Quote(identity) + " FROM " + source,
		"DROP TRIGGER temp." + triggerName,
		"DROP TABLE " + queue,
	}
}

func rebuildProtocolTables(t *testing.T, s *Session, original contract.SchemaDesignSnapshot) []rebuildProtocolTable {
	t.Helper()
	var tables []rebuildProtocolTable
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type != "table" {
			continue
		}
		var table schemaddl.Table
		if e.Name == "sqlite_sequence" {
			table = schemaddl.Table{Name: e.Name, Clauses: []schemaddl.Clause{{ID: "name", Kind: "column", Name: "name"}, {ID: "seq", Kind: "column", Name: "seq"}}}
		} else {
			var err error
			table, err = schemaddl.ParseSQLite(e.SQL)
			if err != nil {
				t.Fatal("protocol needs a complete representable original table", e.Name, err)
			}
		}
		if e.Name == original.Target.Object.Name {
			table = original.Schema.Table.Clone()
		}
		p := rebuildProtocolTable{table: table, source: "main." + schemaddl.Quote(e.Name), saved: "temp." + schemaddl.Quote(fmt.Sprintf("__nyadb_proto_rows_%d", len(tables))), identity: rebuildProtocolIdentity(t, table)}
		for i, c := range table.Columns() {
			p.columns = append(p.columns, schemaddl.Quote(c.Name))
			p.savedCols = append(p.savedCols, schemaddl.Quote(fmt.Sprintf("c%d", i)))
		}
		metadata, err := s.request(context.Background(), "/db/query", []any{"PRAGMA main.foreign_key_list(" + schemaddl.Quote(e.Name) + ")"}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := decodedRows(metadata, 0, 8)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			parent, err := cellText(row[2])
			if err != nil {
				t.Fatal(err)
			}
			p.parents = append(p.parents, sqliteSchemaIdentity(parent))
		}
		tables = append(tables, p)
	}
	return tables
}

// Delete children before parents where possible. Cycles are kept visible to
// SQLite: deferred RESTRICT/CASCADE cycles can settle, while an impossible
// immediate NOT NULL / SET NULL cycle must fail and preserve all originals.
func rebuildProtocolDeleteOrder(tables []rebuildProtocolTable) []rebuildProtocolTable {
	remaining := append([]rebuildProtocolTable(nil), tables...)
	var out []rebuildProtocolTable
	for len(remaining) > 0 {
		at := -1
		for i, candidate := range remaining {
			incoming := false
			for j, child := range remaining {
				if i == j {
					continue
				}
				for _, parent := range child.parents {
					incoming = incoming || parent == sqliteSchemaIdentity(candidate.table.Name)
				}
			}
			if !incoming {
				at = i
				break
			}
		}
		if at < 0 {
			at = 0
		}
		out = append(out, remaining[at])
		remaining = append(remaining[:at], remaining[at+1:]...)
	}
	return out
}

func rebuildProtocolPrograms(t *testing.T, tables []rebuildProtocolTable, desired schemaddl.Table, catalog *schemaddl.SQLiteCatalog) []string {
	t.Helper()
	var sql []string
	for _, e := range catalog.Entries {
		if e.Type == "view" {
			sql = append(sql, "EXPLAIN SELECT * FROM main."+schemaddl.Quote(e.Name)+" LIMIT 0")
		}
	}
	for _, p := range tables {
		if p.table.Name == "sqlite_sequence" {
			continue
		}
		table := p.table
		if table.Name == desired.Name {
			table = desired
		}
		writable, err := schemaddl.SQLiteWritableColumns(table)
		if err != nil {
			t.Fatal(err)
		}
		var columns, nulls, updates []string
		for _, c := range writable {
			name := schemaddl.Quote(c.Name)
			columns, nulls, updates = append(columns, name), append(nulls, "NULL"), append(updates, name+"="+name)
		}
		sql = append(sql,
			"EXPLAIN INSERT INTO "+p.source+" ("+strings.Join(columns, ",")+") SELECT "+strings.Join(nulls, ",")+" WHERE 0",
			"EXPLAIN DELETE FROM "+p.source+" WHERE 0",
			"EXPLAIN UPDATE "+p.source+" SET "+strings.Join(updates, ",")+" WHERE 0")
	}
	return sql
}

func rebuildProtocolBatch(t *testing.T, s *Session, original contract.SchemaDesignSnapshot, desired schemaddl.Table, tables []rebuildProtocolTable) ([]string, []int) {
	t.Helper()
	plan, err := schemaddl.PlanSQLite(original.Schema, desired, "main", false)
	if err != nil || plan.Mode != "rebuild" {
		t.Fatal("protocol requires an original rebuild plan", plan.Mode, err)
	}
	guard, err := s.schemaSnapshotGuardScoped(original, s.dataScope)
	if err != nil {
		t.Fatal(err)
	}
	sql := []string{"CREATE TEMP TABLE __nyadb_proto_guard(ok INTEGER NOT NULL CHECK(ok=1),wfk INTEGER,wdefer INTEGER,wlegacy INTEGER)", "INSERT INTO __nyadb_proto_guard(ok,wfk,wdefer,wlegacy) SELECT 1,(SELECT foreign_keys FROM pragma_foreign_keys),(SELECT defer_foreign_keys FROM pragma_defer_foreign_keys),(SELECT legacy_alter_table FROM pragma_legacy_alter_table)"}
	var guards []int
	addGuard := func(condition string) {
		guards = append(guards, len(sql))
		sql = append(sql, "INSERT INTO __nyadb_proto_guard(ok) SELECT ("+condition+")")
	}
	addGuard(guard)
	addGuard("(SELECT foreign_keys FROM pragma_foreign_keys)=1 AND (SELECT defer_foreign_keys FROM pragma_defer_foreign_keys)=0")
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)")
	addGuard("NOT EXISTS(SELECT 1 FROM temp.sqlite_schema WHERE type IN ('view','trigger'))")
	sql = append(sql, rebuildProtocolPrograms(t, tables, original.Schema.Table, original.Schema.SQLite)...)
	for i, p := range tables {
		sql = append(sql, "CREATE TEMP TABLE "+p.saved+" ("+strings.Join(p.savedCols, ",")+")")
		source, destination := append([]string(nil), p.columns...), append([]string(nil), p.savedCols...)
		if p.identity != "" {
			source = append([]string{schemaddl.Quote(p.identity)}, source...)
			destination = append([]string{"_rowid_"}, destination...)
		}
		sql = append(sql, rebuildProtocolTransfer(p.source, p.saved, source, destination, p.identity, fmt.Sprintf("__nyadb_proto_capture_%d", i))...)
	}
	sql = append(sql, "PRAGMA defer_foreign_keys=ON")
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "trigger" {
			sql = append(sql, "DROP TRIGGER main."+schemaddl.Quote(e.Name))
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "view" {
			sql = append(sql, "DROP VIEW main."+schemaddl.Quote(e.Name))
		}
	}
	for _, p := range rebuildProtocolDeleteOrder(tables) {
		if p.table.Name != "sqlite_sequence" {
			sql = append(sql, "DELETE FROM "+p.source)
		}
	}
	create, err := schemaddl.CreateSQLite(desired, "main", desired.Name)
	if err != nil {
		t.Fatal(err)
	}
	sql = append(sql, "DROP TABLE main."+schemaddl.Quote(desired.Name), create)
	for i, p := range tables {
		if p.table.Name == "sqlite_sequence" {
			continue
		}
		table := p.table
		if table.Name == desired.Name {
			table = desired
		}
		writable, err := schemaddl.SQLiteWritableColumns(table)
		if err != nil {
			t.Fatal(err)
		}
		var source, destination []string
		identity := rebuildProtocolIdentity(t, table)
		if identity != "" {
			source, destination = append(source, "_rowid_"), append(destination, schemaddl.Quote(identity))
		}
		for _, c := range writable {
			for j, old := range p.table.Columns() {
				if c.ID == old.ID {
					source, destination = append(source, p.savedCols[j]), append(destination, schemaddl.Quote(c.Name))
					break
				}
			}
		}
		sql = append(sql, rebuildProtocolTransfer(p.saved, p.source, source, destination, "_rowid_", fmt.Sprintf("__nyadb_proto_restore_%d", i))...)
	}
	for _, p := range tables {
		if p.table.Name == "sqlite_sequence" {
			sql = append(sql, "DELETE FROM main.sqlite_sequence", "INSERT INTO main.sqlite_sequence(_rowid_,name,seq) SELECT _rowid_,c0,c1 FROM "+p.saved)
		}
	}
	for _, d := range desired.Definitions {
		if d.Kind == "index" {
			statement, err := schemaddl.DefinitionSQLite(d, "main", desired.Name)
			if err != nil {
				t.Fatal(err)
			}
			sql = append(sql, statement)
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "view" {
			sql = append(sql, e.SQL)
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "trigger" && e.Table != desired.Name {
			sql = append(sql, e.SQL)
		}
	}
	for _, d := range desired.Definitions {
		if d.Kind == "trigger" {
			statement, err := schemaddl.DefinitionSQLite(d, "main", desired.Name)
			if err != nil {
				t.Fatal(err)
			}
			sql = append(sql, statement)
		}
	}
	sql = append(sql, rebuildProtocolPrograms(t, tables, desired, original.Schema.SQLite)...)
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)")
	for i, p := range tables {
		var saved, current []string
		if p.identity != "" {
			saved = append(saved, "_rowid_")
			identity := p.identity
			if p.table.Name == desired.Name {
				identity = rebuildProtocolIdentity(t, desired)
			}
			current = append(current, schemaddl.Quote(identity))
		}
		for j, old := range p.table.Columns() {
			name := old.Name
			if p.table.Name == desired.Name {
				name = ""
				for _, c := range desired.Columns() {
					if c.ID == old.ID {
						name = c.Name
					}
				}
			}
			if name != "" {
				saved, current = append(saved, p.savedCols[j]), append(current, schemaddl.Quote(name))
			}
		}
		frames := "temp." + schemaddl.Quote(fmt.Sprintf("__nyadb_proto_frames_%d", i))
		sql = append(sql, "CREATE TEMP TABLE "+frames+"(token TEXT COLLATE BINARY NOT NULL,n INTEGER NOT NULL)", "INSERT INTO "+frames+" "+schemaCopyGroups(saved, p.saved))
		addGuard(schemaCopyEquality(frames, schemaCopyGroups(current, p.source)))
		sql = append(sql, "DROP TABLE "+frames, "DROP TABLE "+p.saved)
	}
	// Clearing deferred counters follows an explicit check of the complete
	// actual FK graph. This setting resets on both commit and rollback.
	sql = append(sql, "PRAGMA defer_foreign_keys=OFF")
	addGuard("(SELECT foreign_keys FROM pragma_foreign_keys)=(SELECT wfk FROM __nyadb_proto_guard WHERE wfk IS NOT NULL) AND (SELECT defer_foreign_keys FROM pragma_defer_foreign_keys)=0 AND (SELECT legacy_alter_table FROM pragma_legacy_alter_table)=(SELECT wlegacy FROM __nyadb_proto_guard WHERE wlegacy IS NOT NULL)")
	sql = append(sql, "DELETE FROM __nyadb_proto_guard WHERE wfk IS NOT NULL RETURNING wfk,wdefer,wlegacy", "DROP TABLE temp.__nyadb_proto_guard")
	return sql, guards
}

func rebuildProtocolWriterState(t *testing.T, s *Session, label string) []contract.Cell {
	t.Helper()
	sql := []any{"CREATE TEMP TABLE __nyadb_proto_observer(wfk INTEGER,wdefer INTEGER,wlegacy INTEGER,wobjects INTEGER)", "INSERT INTO __nyadb_proto_observer SELECT (SELECT foreign_keys FROM pragma_foreign_keys),(SELECT defer_foreign_keys FROM pragma_defer_foreign_keys),(SELECT legacy_alter_table FROM pragma_legacy_alter_table),(SELECT count(*) FROM temp.sqlite_schema WHERE name LIKE '__nyadb_proto_%' AND name<>'__nyadb_proto_observer')", "DELETE FROM __nyadb_proto_observer RETURNING wfk,wdefer,wlegacy,wobjects", "DROP TABLE temp.__nyadb_proto_observer"}
	r, err := s.request(context.Background(), "/db/execute", sql, true, true)
	retainNativeFileReceipt(t, label+"-writer-state", r.Raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodedRows(r, 2, 4)
	if err != nil || len(rows) != 1 {
		t.Fatal("complete original writer observer missing", err)
	}
	return rows[0]
}

func rebuildProtocolFingerprints(t *testing.T, l *queryLease, tables []rebuildProtocolTable) map[string][][]contract.Cell {
	t.Helper()
	all := make(map[string][][]contract.Cell, len(tables))
	for _, p := range tables {
		columns := append([]string(nil), p.columns...)
		if p.identity != "" {
			columns = append([]string{schemaddl.Quote(p.identity)}, columns...)
		}
		out := nativeExecute(t, l, schemaCopyGroups(columns, p.source)+" ORDER BY token COLLATE BINARY")
		all[p.table.Name] = out.Sets[0].Rows
	}
	return all
}

func TestNativeRQLiteRebuildProtocolPreservesOriginalDatabaseWithoutLegacySetting(t *testing.T) {
	for _, c := range []struct {
		name, setup, replacement string
		rollback                 bool
	}{
		{"typed_generated", `CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v ANY,r REAL,g TEXT GENERATED ALWAYS AS (hex(v)) STORED) STRICT;INSERT INTO target(id,v,r) VALUES(9007199254740993,X'00FF',1.0000000000000002),(42,CAST(X'610062' AS TEXT),5e-324),(91,CAST(X'80FF' AS TEXT),-1.7976931348623157e308);UPDATE sqlite_sequence SET seq=9223372036854775807 WHERE name='target';INSERT INTO sqlite_sequence(rowid,name,seq) VALUES(33,NULL,X'00FF'),(34,'opaque',CAST(X'80FF' AS TEXT)),(35,'opaque',1.0);CREATE INDEX target_v ON target(v);CREATE TABLE audit(v);CREATE TRIGGER target_insert AFTER INSERT ON target BEGIN INSERT INTO audit VALUES(new.v);END;CREATE VIEW vv AS SELECT id,v FROM target;CREATE VIEW vvv AS SELECT * FROM vv;CREATE TRIGGER vv_delete INSTEAD OF DELETE ON vv BEGIN DELETE FROM target WHERE id=old.id;END;CREATE TRIGGER vv_insert INSTEAD OF INSERT ON vv BEGIN INSERT INTO target(id,v) VALUES(new.id,new.v);END;CREATE TRIGGER vv_update INSTEAD OF UPDATE OF v ON vv BEGIN UPDATE target SET v=new.v WHERE id=old.id;END`, "", false},
		{"recursive_cascade", `CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO target VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE child(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES target(id) ON DELETE CASCADE,v BLOB);INSERT INTO child VALUES(7,42,X'00FE');UPDATE sqlite_sequence SET seq=1000 WHERE name='child';CREATE TABLE grandchild(id INTEGER PRIMARY KEY,parent INTEGER NOT NULL REFERENCES child(id) ON DELETE SET NULL,v BLOB);INSERT INTO grandchild VALUES(9,7,X'00AB');CREATE TABLE audit(v BLOB);CREATE TRIGGER child_delete AFTER DELETE ON child BEGIN INSERT INTO audit VALUES(old.v);END;CREATE TRIGGER child_insert AFTER INSERT ON child BEGIN INSERT INTO audit VALUES(new.v);END;CREATE VIEW vv AS SELECT target.id,grandchild.v FROM target JOIN child ON child.parent=target.id JOIN grandchild ON grandchild.parent=child.id`, "", false},
		{"cyclic_restrict", `CREATE TABLE target(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES child(id) ON DELETE RESTRICT,v BLOB);CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id) ON DELETE RESTRICT,v BLOB);PRAGMA defer_foreign_keys=ON;INSERT INTO target VALUES(1,2,X'00FF');INSERT INTO child VALUES(2,1,X'00FE')`, "", false},
		{"cyclic_cascade", `CREATE TABLE target(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES child(id) ON DELETE CASCADE,v BLOB);CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id) ON DELETE CASCADE,v BLOB);PRAGMA defer_foreign_keys=ON;INSERT INTO target VALUES(1,2,X'00FF');INSERT INTO child VALUES(2,1,X'00FE')`, "", false},
		{"without_rowid", `CREATE TABLE target(k TEXT COLLATE NOCASE,v BLOB,PRIMARY KEY(k)) WITHOUT ROWID;INSERT INTO target VALUES('A',X'00FF'),('b',X'00FE');CREATE TABLE child(id INTEGER PRIMARY KEY,parent TEXT COLLATE NOCASE REFERENCES target(k) ON DELETE RESTRICT);INSERT INTO child VALUES(7,'a')`, "", false},
		{"duplicates_and_aliases", `CREATE TABLE target(token,n,frames,current,v COLLATE NOCASE);INSERT INTO target(rowid,token,n,frames,current,v) VALUES(-9,'same',1,'f','c','A'),(42,'same',1,'f','c','A'),(91,'same',2,'f','c','a'),(99,'same',3,'f','c',X'41'),(111,'same',4,'f','c',NULL),(123,'same',5,'f','c',1),(125,'same',6,'f','c',1.0)`, "", false},
		{"constraint_rollback", `CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO target VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id) ON DELETE CASCADE);INSERT INTO child VALUES(7,42);CREATE VIEW vv AS SELECT * FROM target`, "BLOB CHECK(length(v)>10)", true},
		{"affinity_rollback", `CREATE TABLE target(id INTEGER PRIMARY KEY,v);INSERT INTO target VALUES(42,1.0),(91,'001');CREATE VIEW vv AS SELECT * FROM target`, "TEXT", true},
		{"foreign_key_rollback", `CREATE TABLE target(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO target VALUES(42,X'00FF');CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id) ON DELETE CASCADE);INSERT INTO child VALUES(7,42);CREATE VIEW vv AS SELECT * FROM target`, "", true},
		{"immediate_cycle_rollback", `CREATE TABLE target(id INTEGER PRIMARY KEY,parent INTEGER NOT NULL REFERENCES child(id) ON DELETE SET NULL,v BLOB);CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER NOT NULL REFERENCES target(id) ON DELETE SET NULL,v BLOB);PRAGMA defer_foreign_keys=ON;INSERT INTO target VALUES(1,2,X'00FF');INSERT INTO child VALUES(2,1,X'00FE')`, "", true},
		{"wide_hidden_rowid", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			profile, _ := nativeFixtureConfigured(t, "", "", true)
			s := openNative(t, profile, "original-fixture-password")
			l := lease(t, s, 725)
			setup := c.setup
			if c.name == "wide_hidden_rowid" {
				var columns, values []string
				for i := 0; i < 2000; i++ {
					columns = append(columns, fmt.Sprintf("c%04d", i))
					value := "X'00FF'"
					if i > 1 {
						value = []string{"NULL", "9223372036854775807", "1.0000000000000002", "CAST(X'610080FF' AS TEXT)", "X'00FF'"}[i%5]
					}
					values = append(values, value)
				}
				setup = "CREATE TABLE target(" + strings.Join(columns, ",") + ");INSERT INTO target(rowid," + strings.Join(columns, ",") + ") VALUES(-9," + strings.Join(values, ",") + ");INSERT INTO target(rowid," + strings.Join(columns, ",") + ") VALUES(9007199254740993," + strings.Join(values, ",") + ")"
			}
			// Cyclic fixtures need one native transaction, including both rows.
			var statements []any
			for _, statement := range strings.Split(setup, ";") {
				if strings.TrimSpace(statement) != "" {
					statements = append(statements, statement)
				}
			}
			// Trigger bodies contain semicolons; normal SQL execution already has
			// a complete statement parser. Only the cycle setup uses this split.
			if strings.Contains(c.name, "cyclic") || c.name == "immediate_cycle_rollback" {
				r, err := s.request(context.Background(), "/db/execute", statements, true, true)
				retainNativeFileReceipt(t, "rebuild-protocol-"+c.name+"-setup", r.Raw)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				nativeExecute(t, l, setup)
			}
			target := contract.SchemaDesignTarget{Generation: s.Generation(), Object: contract.ObjectRef{ProfileID: profile.ID, Catalog: "main", Kind: "table", Name: "target"}}
			original, err := s.SchemaDesign().Snapshot(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			desired := original.Schema.Table.Clone()
			// Keep stable IDs while changing physical column order and a complete
			// declaration. New defaults are omitted from the original transfer.
			desired.Clauses[0], desired.Clauses[1] = desired.Clauses[1], desired.Clauses[0]
			for i, clause := range desired.Clauses {
				if clause.Name == "v" && c.replacement != "" {
					desired.Clauses[i].SQL = c.replacement
				}
				if clause.Name == "r" && c.name == "typed_generated" {
					desired.Clauses[i].SQL = "REAL NOT NULL DEFAULT (1.25)"
				}
				if clause.Name == "v" && c.name == "recursive_cascade" {
					desired.Clauses[i].SQL = "BLOB DEFAULT X'00FA'"
				}
				if clause.Name == "v" && c.name == "duplicates_and_aliases" {
					desired.Clauses[i].SQL = "BLOB COLLATE NOCASE DEFAULT X'00AA'"
				}
				if clause.Name == "id" && c.name == "foreign_key_rollback" {
					desired.Clauses[i].SQL = "INTEGER"
				}
			}
			if c.name != "wide_hidden_rowid" {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:default", Kind: "column", Name: "added", SQL: "BLOB DEFAULT X'00AB'"})
			} else {
				desired.Clauses[0].SQL = "BLOB DEFAULT X'00AB'"
			}
			if !c.rollback {
				check := "CHECK(id>0)"
				switch c.name {
				case "without_rowid":
					check = "CHECK(length(k)>0)"
				case "duplicates_and_aliases":
					check = "CHECK(n>0)"
				case "wide_hidden_rowid":
					check = "CHECK(length(c0000)=2)"
				}
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:constraint", Kind: "constraint", SQL: check})
			}
			if _, err := schemaddl.PlanSQLiteNative(original.Schema, desired, "main", false); err == nil {
				t.Fatal("protocol evidence prematurely published a rebuild executor")
			}
			tables := rebuildProtocolTables(t, s, original)
			before := rebuildProtocolFingerprints(t, l, tables)
			beforeState := rebuildProtocolWriterState(t, s, "rebuild-protocol-"+c.name+"-before")
			sql, guards := rebuildProtocolBatch(t, s, original, desired, tables)
			if len(sql) > 1024 || strings.Contains(strings.Join(sql, ";"), "PRAGMA legacy_alter_table=") || strings.Contains(strings.Join(sql, ";"), "PRAGMA foreign_keys=") {
				t.Fatal("protocol exceeds native bounds or changes nontransactional writer settings")
			}
			statements = make([]any, len(sql))
			for i, statement := range sql {
				statements[i] = statement
			}
			r, executeErr := s.request(context.Background(), "/db/execute", statements, true, true)
			retainNativeFileReceipt(t, "rebuild-protocol-"+c.name+"-native", r.Raw)
			requestJSON, err := json.Marshal(struct {
				Original   contract.SchemaDesignSnapshot
				Desired    schemaddl.Table
				Statements []string
				Guards     []int
			}{original, desired, sql, guards})
			if err != nil {
				t.Fatal(err)
			}
			retainNativeFileReceipt(t, "rebuild-protocol-"+c.name+"-request", string(requestJSON))
			if c.rollback {
				if executeErr == nil || r.Uncertain || len(r.Results) >= len(sql) || !r.Results[len(r.Results)-1].failed() {
					t.Fatal("protocol failure did not acknowledge rollback", executeErr, r.Uncertain)
				}
			} else {
				if executeErr != nil || r.Uncertain || len(r.Results) != len(sql) {
					t.Fatal("protocol did not complete every original statement", executeErr, r.Uncertain)
				}
				for _, at := range guards {
					n, err := r.Results[at].Affected.Int64()
					if err != nil || n != 1 {
						t.Fatal("complete native preservation guard not acknowledged", at, err)
					}
				}
			}
			afterState := rebuildProtocolWriterState(t, s, "rebuild-protocol-"+c.name+"-after")
			if !reflect.DeepEqual(beforeState, afterState) || beforeState[0].Text != "1" || beforeState[1].Text != "0" || beforeState[2].Text != "0" || beforeState[3].Text != "0" {
				t.Fatal("original writer settings or private TEMP state changed", beforeState, afterState)
			}
			after := rebuildProtocolFingerprints(t, l, tables)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("protocol changed original complete values, storage classes, physical rowids, multiplicity or sequence frames")
			}
			actual, err := s.SchemaDesign().Snapshot(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			verificationJSON, err := json.Marshal(struct {
				Original, Actual            contract.SchemaDesignSnapshot
				Before, After               map[string][][]contract.Cell
				WriterBefore, WriterAfter   []contract.Cell
				Rollback, PublicationProved bool
			}{original, actual, before, after, beforeState, afterState, c.rollback, false})
			if err != nil {
				t.Fatal(err)
			}
			retainNativeFileReceipt(t, "rebuild-protocol-"+c.name+"-verification", string(verificationJSON))
			if c.rollback {
				if !reflect.DeepEqual(original, actual) {
					t.Fatal("rollback changed original complete catalog or snapshot")
				}
			} else {
				if !schemaddl.SQLiteNativeTableMatches(actual.Schema.Table, desired, "main") {
					t.Fatal("native reconstructed definition differs from desired complete schema")
				}
				for _, e := range original.Schema.SQLite.Entries {
					if e.Table == desired.Name {
						continue
					}
					found := false
					for _, a := range actual.Schema.SQLite.Entries {
						found = found || reflect.DeepEqual(e, a)
					}
					if !found {
						t.Fatal("protocol changed an unselected complete catalog entry", e.Name)
					}
				}
				if c.name != "wide_hidden_rowid" {
					out := nativeExecute(t, l, "SELECT count(*) FROM target WHERE typeof(added)<>'blob' OR hex(added)<>'00AB'")
					if out.Sets[0].Rows[0][0].Text != "0" {
						t.Fatal("new complete default was not applied")
					}
				}
				if c.name == "typed_generated" {
					nativeExecute(t, l, "UPDATE vv SET v=X'00AC' WHERE id=42;DELETE FROM vv WHERE id=91;INSERT INTO vv(id,v) VALUES(99,X'00AD')")
					out := nativeExecute(t, l, "SELECT count(*) FROM target WHERE (id=42 AND hex(v)='00AC') OR (id=99 AND hex(v)='00AD');SELECT count(*) FROM target WHERE id=91;SELECT count(*) FROM audit")
					if out.Sets[0].Rows[0][0].Text != "2" || out.Sets[1].Rows[0][0].Text != "0" || out.Sets[2].Rows[0][0].Text != "1" {
						t.Fatal("restored INSTEAD OF or original table trigger program is not usable")
					}
				}
			}
			t.Logf("native reconstruction protocol case=%s rollback=%t statements=%d guards=%d original_tables=%d complete_values_and_catalog_verified=true writer_settings_preserved=true publication_proved=false", c.name, c.rollback, len(sql), len(guards), len(tables))
		})
	}
}
