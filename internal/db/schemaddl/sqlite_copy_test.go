package schemaddl

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSQLiteCopyPreservesFullClausesAndBodiesWhileRebindingOnlyIdentities(t *testing.T) {
	original, err := ParseSQLite(`CREATE TABLE "source" (id INTEGER PRIMARY KEY, parent INTEGER REFERENCES "SOURCE"(id) ON DELETE CASCADE, external INTEGER REFERENCES other(id), label TEXT DEFAULT 'REFERENCES source; ON source', gen TEXT GENERATED ALWAYS AS (label || ' source') STORED, CONSTRAINT self_fk FOREIGN KEY(external) REFERENCES source(id) DEFERRABLE INITIALLY DEFERRED) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	original.Definitions = []Definition{
		{Name: "source_expr", Kind: "index", SQL: `CREATE UNIQUE INDEX "source_expr" ON "source"(lower(label) COLLATE NOCASE DESC) WHERE label <> 'source'`},
		{Name: "source_trigger", Kind: "trigger", SQL: `CREATE TRIGGER "source_trigger" AFTER UPDATE OF label ON source WHEN new.label='ON source' BEGIN UPDATE source SET label='original body; source' WHERE id=old.id; END`},
	}
	before := Signature(original)
	copied, err := CopySQLite(original, "aux", `copied"name`, true)
	if err != nil {
		t.Fatal(err)
	}
	if Signature(original) != before {
		t.Fatal("copy mutated the source draft")
	}
	if copied.Options != "STRICT" || copied.Name != `copied"name` || len(copied.Clauses) != len(original.Clauses) {
		t.Fatal("table properties lost")
	}
	if copied.Clauses[1].SQL != `INTEGER REFERENCES "copied""name"(id) ON DELETE CASCADE` || copied.Clauses[2].SQL != original.Clauses[2].SQL || copied.Clauses[3].SQL != original.Clauses[3].SQL || copied.Clauses[4].SQL != original.Clauses[4].SQL {
		t.Fatalf("references/literals rewritten incorrectly: %+v", copied.Clauses)
	}
	if !strings.Contains(copied.Clauses[5].SQL, `REFERENCES "copied""name"(id)`) {
		t.Fatal("table self-FK not rebound")
	}
	for i, d := range copied.Definitions {
		if !strings.Contains(d.SQL, `ON "copied""name"`) {
			t.Fatal("ON target not copied", d.SQL)
		}
		if _, err := DefinitionSQLite(d, "aux", copied.Name); err != nil {
			t.Fatal(err)
		}
		if i == 1 && !strings.Contains(d.SQL, `BEGIN UPDATE source SET label='original body; source' WHERE id=old.id; END`) {
			t.Fatal("trigger body changed", d.SQL)
		}
	}
	unchanged, err := CopySQLite(original, "aux", "other_copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if Signature(unchanged.Clauses) != Signature(original.Clauses) {
		t.Fatal("preserve-reference choice changed clauses")
	}
}

func TestSQLiteCopyRejectsWrongDefinitionIdentityAndNeverSilentlyMergesDestination(t *testing.T) {
	table, err := ParseSQLite("CREATE TABLE t(id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"T", "", "sqlite_reserved", "with\x00nul"} {
		if _, err := CopySQLite(table, "main", destination, true); err == nil {
			t.Fatal("unsafe destination accepted", destination)
		}
	}
	table.Definitions = []Definition{{Name: "idx", Kind: "index", SQL: "CREATE INDEX idx ON another_table(id)"}}
	if _, err := CopySQLite(table, "main", "copy", true); err == nil {
		t.Fatal("definition for another table copied")
	}
	table.Definitions = []Definition{{Name: "idx", Kind: "index", SQL: "CREATE INDEX idx ON t(id); DROP TABLE t"}}
	if _, err := CopySQLite(table, "main", "copy", true); err == nil {
		t.Fatal("multi statement copied")
	}
}

func TestSQLiteCopiedDefinitionNamesAreStableBoundedAndUTF8Valid(t *testing.T) {
	destination := strings.Repeat("复", 300)
	one := sqliteCopyDefinitionName(destination, strings.Repeat("索", 200))
	two := sqliteCopyDefinitionName(destination, strings.Repeat("索", 199)+"引")
	if len(one) > 1024 || !utf8.ValidString(one) || one == two || one != sqliteCopyDefinitionName(destination, strings.Repeat("索", 200)) {
		t.Fatal("invalid/colliding copied definition name")
	}
}
