package schemaddl

import (
	"strings"
	"testing"
)

func TestSQLiteViewParsesCompleteColumnsCTEAndLiteralDelimiters(t *testing.T) {
	v, err := ParseSQLiteView(`CREATE VIEW "a""ux"."v"("I","K") AS WITH x(value) AS (VALUES('literal; AS CREATE VIEW')) SELECT value, 'ON v' FROM x; -- trailing comment`)
	if err != nil || v.Schema != `a"ux` || v.Name != "v" || len(v.Columns) != 2 || !strings.Contains(v.SelectSQL, "'literal; AS CREATE VIEW'") {
		t.Fatalf("parse: %+v %v", v, err)
	}
	sql, err := CreateSQLiteView(v, `a"ux`)
	if err != nil || !strings.Contains(sql, `"a""ux"."v"`) {
		t.Fatal(sql, err)
	}
	for _, ddl := range []string{`CREATE TEMP VIEW v AS SELECT 1`, `CREATE VIEW v AS SELECT 1;DROP TABLE t`, `CREATE VIEW v(a,A) AS SELECT 1,2`, `CREATE VIEW v AS DELETE FROM t`, `CREATE VIEW aux.v AS SELECT 1; SELECT 2`} {
		if _, err := ParseSQLiteView(ddl); err == nil {
			t.Fatal("unsafe view accepted", ddl)
		}
	}
}

func TestSQLiteViewPlanPreservesTriggerBodyAndRejectsHiddenTriggerChanges(t *testing.T) {
	v, err := ParseSQLiteView(`CREATE VIEW v(id,value) AS SELECT id,value FROM source`)
	if err != nil {
		t.Fatal(err)
	}
	v.Triggers = []Definition{{Kind: "trigger", Name: "view_write", SQL: `CREATE TRIGGER view_write INSTEAD OF UPDATE OF value ON v BEGIN UPDATE source SET value=new.value WHERE id=new.id; END`}}
	snapshot := ViewSnapshot{Engine: "sqlite", Revision: "source", Exists: true, View: v}
	desired := v.Clone()
	desired.Name = "renamed"
	plan, err := PlanSQLiteView(snapshot, "rename", desired, "aux")
	if err != nil || len(plan.SQL) != 3 || !strings.Contains(plan.SQL[2], `ON "renamed"`) || !strings.Contains(plan.SQL[2], `BEGIN UPDATE source SET value=new.value WHERE id=new.id; END`) {
		t.Fatalf("rename plan: %+v %v", plan, err)
	}
	if snapshot.View.Triggers[0].SQL != v.Triggers[0].SQL || snapshot.View.Name != "v" {
		t.Fatal("source mutated")
	}
	desired = v.Clone()
	desired.Triggers[0].SQL = "CREATE TRIGGER hidden INSTEAD OF DELETE ON v BEGIN DELETE FROM source;END"
	if _, err := PlanSQLiteView(snapshot, "alter", desired, "aux"); err == nil {
		t.Fatal("hidden trigger replacement accepted")
	}
	desired = v.Clone()
	desired.Name = "renamed"
	desired.SelectSQL = "SELECT 2"
	if _, err := PlanSQLiteView(snapshot, "rename", desired, "aux"); err == nil {
		t.Fatal("rename changed query")
	}
	desired = v.Clone()
	desired.Schema = "other"
	if _, err := PlanSQLiteView(snapshot, "alter", desired, "aux"); err == nil {
		t.Fatal("namespace changed")
	}
	if _, err := PlanSQLiteView(snapshot, "unknown", v, "aux"); err == nil {
		t.Fatal("unknown action accepted")
	}
}
