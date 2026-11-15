package schemaddl

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSQLiteNativeCapacityCountsColumnsSeparatelyFromTableConstraints(t *testing.T) {
	columns := make([]string, 2000)
	for i := range columns {
		columns[i] = fmt.Sprintf("c%04d TEXT", i)
	}
	table, err := ParseSQLite("CREATE TABLE wide(" + strings.Join(columns, ",") + ",CONSTRAINT complete_check CHECK(c0000 IS NOT NULL))")
	if err != nil || len(table.Columns()) != 2000 || len(table.Clauses) != 2001 {
		t.Fatal("full native column capacity lost table-level constraints", err)
	}
	if _, err := ParseSQLite("CREATE TABLE too_wide(" + strings.Join(columns, ",") + ",overflow TEXT)"); err == nil {
		t.Fatal("native SQLite column capacity overflow accepted")
	}
	constraints := strings.Repeat(",CHECK(id>0)", 1000)
	if _, err := ParseSQLite("CREATE TABLE constrained(id INTEGER" + constraints + ")"); err != nil {
		t.Fatal("complete bounded table constraints were confused with columns", err)
	}
	if _, err := ParseSQLite("CREATE TABLE constrained(id INTEGER" + constraints + ",CHECK(id>0))"); err == nil {
		t.Fatal("table constraint capacity overflow accepted")
	}
}

func nativeSQLiteOriginal(t *testing.T) Snapshot {
	t.Helper()
	table, err := ParseSQLite(`CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''',gone BLOB,CONSTRAINT ck CHECK(length(v)>0))`)
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Engine: "sqlite", Exists: true, Revision: "sealed", DDL: `CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''',gone BLOB,CONSTRAINT ck CHECK(length(v)>0))`, Table: table, SQLite: &SQLiteCatalog{Version: 12, Entries: []SQLiteCatalogEntry{{Type: "table", Name: "original", Table: "original", Root: 2, SQL: `CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''',gone BLOB,CONSTRAINT ck CHECK(length(v)>0))`}, {Type: "table", Name: "unselected", Table: "unselected", Root: 3, SQL: `CREATE TABLE unselected(id INTEGER)`}}}}
}
func TestSQLiteNativeColumnPlanPreservesStableColumnsConstraintsAndAppendOrder(t *testing.T) {
	original := nativeSQLiteOriginal(t)
	desired := original.Table.Clone()
	desired.Clauses = append(desired.Clauses[:2], desired.Clauses[3:]...)
	desired.Clauses = append(desired.Clauses, Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "BLOB DEFAULT X'00ff'"})
	p, err := PlanSQLiteNative(original, desired, "main", true)
	if err != nil || p.Mode != "native_columns" || !reflect.DeepEqual(p.Dropped, []string{"gone"}) || len(p.SQL) != 2 || strings.Contains(strings.Join(p.SQL, ";"), "DROP TABLE") || !strings.Contains(p.SQL[1], "DEFAULT X'00ff'") {
		t.Fatal("native plan became a shadow rebuild or lost typed definition", err, p)
	}
	if _, err := PlanSQLiteNative(original, desired, "main", false); err == nil {
		t.Fatal("column drop bypassed explicit data-loss confirmation")
	}
	for _, edit := range []func(*Table){
		func(t *Table) { t.Clauses[0].SQL = "TEXT PRIMARY KEY" },
		func(t *Table) { t.Clauses[0], t.Clauses[1] = t.Clauses[1], t.Clauses[0] },
		func(t *Table) { t.Clauses[2].SQL = "CHECK(1)" },
		func(t *Table) { t.Clauses[0], t.Clauses[3] = t.Clauses[3], t.Clauses[0] },
		func(t *Table) { t.Options = "STRICT" },
		func(t *Table) {
			t.Definitions = append(t.Definitions, Definition{Name: "ix", Kind: "index", SQL: "CREATE INDEX ix ON original(v)"})
		},
		func(t *Table) { t.Clauses[0].ID = "new:replacement" },
		func(t *Table) { t.OpenGauss = &OpenGaussTableProperties{} },
	} {
		changed := desired.Clone()
		edit(&changed)
		if _, err := PlanSQLiteNative(original, changed, "main", true); err == nil {
			t.Fatal("rebuild-only or metadata change became native column plan", changed)
		}
	}
}

func TestSQLiteNativeWritableColumnsKeepGeneratedAndLiteralGrammarDistinct(t *testing.T) {
	table, err := ParseSQLite(`CREATE TABLE t(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'AS (literal)',g TEXT GENERATED ALWAYS AS (v||'AS') STORED,h TEXT AS (v) VIRTUAL,CONSTRAINT ck CHECK(length(v)>0))`)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := SQLiteWritableColumns(table)
	if err != nil || len(columns) != 2 || columns[1].Name != "v" {
		t.Fatal("literal or generated grammar flattened", err, columns)
	}
}

func TestSQLiteNativeCatalogVerifiesCompleteRenameAndUnselectedContext(t *testing.T) {
	original := nativeSQLiteOriginal(t)
	original.Table.Definitions = []Definition{{Name: "ix", Kind: "index", SQL: `CREATE INDEX ix ON original(v)`}}
	original.SQLite.Entries = append([]SQLiteCatalogEntry{{Type: "index", Name: "ix", Table: "original", Root: 4, SQL: `CREATE INDEX ix ON original(v)`}}, original.SQLite.Entries...)
	desired := original.Table.Clone()
	desired.Clauses[1].Name = "label"
	p, err := PlanSQLiteNative(original, desired, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	actual := original.Clone()
	actual.Table = desired.Clone()
	actual.Table.Clauses[3].SQL = `CONSTRAINT ck CHECK(length("label")>0)`
	actual.Table.Definitions[0].SQL = `CREATE INDEX ix ON original("label")`
	actual.SQLite.Entries[0].SQL = actual.Table.Definitions[0].SQL
	actual.SQLite.Entries[1].SQL = `CREATE TABLE original(id INTEGER PRIMARY KEY,"label" TEXT DEFAULT 'X''61''',gone BLOB,CONSTRAINT ck CHECK(length("label")>0))`
	if err := VerifySQLiteNativeCatalog(original, desired, actual, p); err != nil {
		t.Fatal("complete native rename rejected", err)
	}
	for _, edit := range []func(*Snapshot){
		func(s *Snapshot) {
			s.SQLite.Entries[1].SQL = strings.Replace(s.SQLite.Entries[1].SQL, "X''61''", "changed", 1)
		},
		func(s *Snapshot) { s.SQLite.Entries[0].Root++ },
		func(s *Snapshot) { s.SQLite.Entries[2].SQL = "CREATE TABLE unselected(id TEXT)" },
		func(s *Snapshot) { s.SQLite.Entries = s.SQLite.Entries[:2] },
		func(s *Snapshot) { s.Table.Clauses[3].SQL = "CHECK(1)" },
		func(s *Snapshot) { s.Table.Definitions = nil },
	} {
		changed := actual.Clone()
		edit(&changed)
		if err := VerifySQLiteNativeCatalog(original, desired, changed, p); err == nil {
			t.Fatal("partial or lossy native rename verified")
		}
	}
}

func TestSQLiteNativeColumnShapeNormalizesNativePlacementWithoutLosingClauses(t *testing.T) {
	original := nativeSQLiteOriginal(t)
	desired := original.Table.Clone()
	desired.Clauses = append(desired.Clauses, Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "TEXT DEFAULT 'native'"})
	p, err := PlanSQLiteNative(original, desired, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	actual := original.Clone()
	sql, err := CreateSQLite(desired, "main", desired.Name)
	if err != nil {
		t.Fatal(err)
	}
	actual.DDL = sql
	actual.Table, err = ParseSQLite(sql)
	if err != nil {
		t.Fatal(err)
	}
	actual.SQLite.Entries[0].SQL = sql
	if err := VerifySQLiteNativeCatalog(original, desired, actual, p); err != nil {
		t.Fatal("native column-before-constraint placement rejected", err)
	}
	actual.Table.Clauses[1].SQL = "TEXT DEFAULT 'lost'"
	if err := VerifySQLiteNativeCatalog(original, desired, actual, p); err == nil {
		t.Fatal("changed literal default verified")
	}
	dropped := original.Clone()
	dropped.Exists = false
	dropped.Table = Table{Name: original.Table.Name}
	dropped.SQLite.Entries = dropped.SQLite.Entries[1:]
	if err := VerifySQLiteNativeDrop(original, dropped); err != nil {
		t.Fatal(err)
	}
	dropped.SQLite.Entries[0].SQL = "changed"
	if !errors.Is(VerifySQLiteNativeDrop(original, dropped), ErrConflict) {
		t.Fatal("drop verified changed unselected schema")
	}
}
