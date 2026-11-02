package schemaddl

import (
	"reflect"
	"strings"
	"testing"
)

func TestSQLiteLosslessTableClausesAndBoundaryValidation(t *testing.T) {
	ddl := "CREATE TABLE [table; name] ([PRIMARY] TEXT DEFAULT 'back\\', `a,b` BLOB, n INTEGER -- retained comment\n, computed TEXT GENERATED ALWAYS AS (printf('%s,%s', [PRIMARY], n)) STORED, CONSTRAINT \"named check\" CHECK (n IN (1,2))) STRICT"
	table, err := ParseSQLite(ddl)
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Clauses) != 5 || table.Clauses[0].Name != "PRIMARY" || table.Clauses[0].Kind != "column" || !strings.Contains(table.Clauses[2].SQL, "-- retained comment") || table.Clauses[4].Kind != "constraint" {
		t.Fatalf("lost clauses: %+v", table)
	}
	rendered, err := CreateSQLite(table, `a"ux`, table.Name)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseSQLite(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(table, reparsed) {
		t.Fatalf("lossy round trip: %+v => %+v", table, reparsed)
	}
	for _, ddl := range []string{
		"CREATE VIRTUAL TABLE x USING fts5(v)", "CREATE TABLE x AS SELECT 1", "CREATE TABLE x (id); DROP TABLE y", "CREATE TABLE x (id INT, ID TEXT)", "CREATE TABLE x (v TEXT DEFAULT 'unterminated)", "CREATE TABLE x (v INT) WITHOUT", "CREATE TABLE x (v INT) STRICT, STRICT", "CREATE TABLE x (v INT) STRICT,", "CREATE TABLE x (v INT) WITH (a=1)",
	} {
		if _, err := ParseSQLite(ddl); err == nil {
			t.Fatalf("accepted unsafe/incomplete table: %s", ddl)
		}
	}
	for _, tail := range []string{"TEXT, injected TEXT", "TEXT); DELETE FROM x; --", "TEXT DEFAULT ((1)", "TEXT DEFAULT 1;"} {
		mutated := table.Clone()
		mutated.Clauses[0].SQL = tail
		if _, err := CreateSQLite(mutated, "main", mutated.Name); err == nil {
			t.Fatalf("accepted escaped column fragment: %s", tail)
		}
	}
}

func TestSQLitePlansUseStableIdentityAndNativeRename(t *testing.T) {
	table, _ := ParseSQLite("CREATE TABLE t(id INTEGER PRIMARY KEY, value TEXT, CHECK (length(value)>0))")
	table.Definitions = []Definition{{Name: "ix", Kind: "index", SQL: "CREATE INDEX ix ON t(value)"}}
	original := Snapshot{Engine: "sqlite", Exists: true, Revision: "revision", Table: table}
	desired := table.Clone()
	desired.Name = "new t"
	p, err := PlanSQLite(original, desired, "main", false)
	if err != nil || p.Mode != "rename_table" || len(p.SQL) != 1 {
		t.Fatalf("rename: %+v %v", p, err)
	}
	desired.Clauses[1].SQL = "BLOB"
	if _, err := PlanSQLite(original, desired, "main", false); err == nil {
		t.Fatal("accepted combined table rename/rebuild")
	}
	desired = table.Clone()
	desired.Clauses[1].Name = "label"
	p, err = PlanSQLite(original, desired, "main", false)
	if err != nil || p.Mode != "rename_column" || p.RenameFrom != "value" {
		t.Fatalf("column rename: %+v %v", p, err)
	}
	desired = table.Clone()
	desired.Clauses = append(desired.Clauses[:1], desired.Clauses[2:]...)
	if _, err := PlanSQLite(original, desired, "main", false); err == nil {
		t.Fatal("column removal was not confirmed")
	}
	p, err = PlanSQLite(original, desired, "main", true)
	if err != nil || !reflect.DeepEqual(p.Dropped, []string{"value"}) {
		t.Fatalf("drop plan: %+v %v", p, err)
	}
	desired = table.Clone()
	desired.Clauses[1].ID = desired.Clauses[0].ID
	if _, err := PlanSQLite(original, desired, "main", true); err == nil {
		t.Fatal("duplicated stable ID")
	}
	desired = table.Clone()
	desired.Options = "WITHOUT ROWID"
	if _, err := PlanSQLite(original, desired, "main", false); err == nil {
		t.Fatal("silently changed row identity mode")
	}
}

func TestSQLiteDefinitionQualificationNeverReplacesBodies(t *testing.T) {
	d := Definition{Name: "tr; name", Kind: "trigger", SQL: "CREATE TRIGGER [tr; name] AFTER UPDATE ON t BEGIN INSERT INTO log VALUES ('t; old.name', CASE WHEN new.x=1 THEN 'END;' ELSE 'BEGIN' END); UPDATE log SET value='t'; END;"}
	sql, err := DefinitionSQLite(d, `a"ux`, "t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `CREATE TRIGGER "a""ux"."tr; name"`) || !strings.Contains(sql, "'t; old.name'") {
		t.Fatal(sql)
	}
	for _, sql := range []string{
		"CREATE TRIGGER [tr; name] AFTER UPDATE ON t BEGIN SELECT 1; END; DROP TABLE t;",
		"CREATE TRIGGER [tr; name] AFTER UPDATE ON other BEGIN SELECT 1; END;",
		"CREATE TRIGGER other.[tr; name] AFTER UPDATE ON t BEGIN SELECT 1; END;",
		"CREATE TEMP TRIGGER [tr; name] AFTER UPDATE ON t BEGIN SELECT 1; END;",
		"CREATE INDEX [tr; name] ON t(x);", "CREATE TRIGGER IF NOT EXISTS [tr; name] AFTER UPDATE ON t BEGIN SELECT 1; END;",
	} {
		d.SQL = sql
		if _, err := DefinitionSQLite(d, "main", "t"); err == nil {
			t.Fatalf("accepted invalid target/boundary: %s", sql)
		}
	}
}

func TestSchemaLexerProtectsPortableQuotedFormsAndLimits(t *testing.T) {
	ts, err := lex(`E'escaped\'quote' 'standard\' $$a;b$$ $tag$END;$tag$ [a;b] /* outer /* inner */ done */ (x)`)
	if err != nil || len(ts) != 8 {
		t.Fatalf("tokens: %+v %v", ts, err)
	}
	if _, err := lex(strings.Repeat("x", (1<<20)+1)); err == nil {
		t.Fatal("oversized input")
	}
	if _, err := lex("/* never closed"); err == nil {
		t.Fatal("unterminated comment")
	}
}

func FuzzSQLiteTableParsing(f *testing.F) {
	f.Add("CREATE TABLE t(id INTEGER PRIMARY KEY, a TEXT DEFAULT 'back\\')")
	f.Add("CREATE TABLE [t] ([a,b] TEXT, CHECK ([a,b] != ';')) STRICT")
	f.Fuzz(func(t *testing.T, source string) {
		table, err := ParseSQLite(source)
		if err != nil {
			return
		}
		sql, err := CreateSQLite(table, "main", table.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSQLite(sql); err != nil {
			t.Fatalf("rendered invalid SQL: %v", err)
		}
	})
}
