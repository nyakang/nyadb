package schemaddl

import (
	"strings"
	"testing"
)

func TestPostgreSQLDefinitionRebindPreservesBodyAndLiteralNames(t *testing.T) {
	d := Definition{Kind: "trigger", Name: "old_trigger", SQL: `CREATE TRIGGER old_trigger BEFORE UPDATE ON demo.old_table FOR EACH ROW EXECUTE FUNCTION demo.fn('old_table','old_trigger')`, Postgres: &PostgreSQLDefinitionProperties{Enabled: "R", Comment: "original"}}
	x, err := RebindPostgreSQLDefinition(d, "demo", "old_table", "probe_table", "probe_trigger")
	if err != nil || !strings.Contains(x.SQL, `CREATE TRIGGER "probe_trigger"`) || !strings.Contains(x.SQL, `ON "demo"."probe_table"`) || !strings.Contains(x.SQL, `fn('old_table','old_trigger')`) {
		t.Fatal("probe rebound user body/literals", x, err)
	}
	x.Postgres.Enabled = "D"
	if d.Postgres.Enabled != "R" {
		t.Fatal("probe mutated original properties")
	}
	if _, err := RebindPostgreSQLDefinition(d, "other", "old_table", "probe_table", "probe_trigger"); err == nil {
		t.Fatal("rebind bypassed original namespace")
	}
}

func TestPostgreSQLConstraintProbeRebindsOnlySelfReference(t *testing.T) {
	for _, entry := range []struct{ reference, expected string }{
		{`demo.old_table`, `"demo"."probe_table"`}, {`external.old_table`, `"external"."old_table"`},
	} {
		c := Clause{ID: "constraint:1", Kind: "constraint", Name: "fk", SQL: `CONSTRAINT fk FOREIGN KEY(parent_id) REFERENCES ` + entry.reference + `(id) DEFERRABLE INITIALLY DEFERRED`}
		x, err := RebindPostgreSQLConstraint(c, "demo", "old_table", "probe_table", "probe_fk")
		if err != nil || !strings.Contains(x.SQL, `REFERENCES `+entry.expected) || x.ID != c.ID {
			t.Fatal(x, err)
		}
	}
	c := Clause{ID: "constraint:2", Kind: "constraint", Name: "ck", SQL: `CONSTRAINT ck CHECK(label<>'old_table')`}
	x, err := RebindPostgreSQLConstraint(c, "demo", "old_table", "probe_table", "probe_ck")
	if err != nil || !strings.Contains(x.SQL, `label<>'old_table'`) {
		t.Fatal("literal was rebound", x, err)
	}
}

func TestPostgreSQLSequenceNameExtractionKeepsExactSettings(t *testing.T) {
	schema, name, remaining, err := PostgreSQLSequenceName(`SEQUENCE NAME "demo"."seq""name" INCREMENT BY -3 START WITH 9007199254740993 MINVALUE -9007199254740993 MAXVALUE 9007199254740999 CACHE 13 NO CYCLE`)
	if err != nil || schema != "demo" || name != `seq"name` || !strings.Contains(remaining, "START WITH 9007199254740993") || strings.Contains(remaining, "SEQUENCE NAME") {
		t.Fatal(schema, name, remaining, err)
	}
	for _, raw := range []string{`SEQUENCE NAME a SEQUENCE NAME b`, `SEQUENCE NAME a.b.c`, `SEQUENCE other`, `SEQUENCE NAME 'text'`, `SEQUENCE NAME a; DROP TABLE b`} {
		if _, _, _, err := PostgreSQLSequenceName(raw); err == nil {
			t.Fatal("invalid identity name acquired binding", raw)
		}
	}
}

func TestPostgreSQLConstraintValidationStateUsesOnlyTrailingClause(t *testing.T) {
	for _, entry := range []struct {
		sql         string
		unvalidated bool
	}{
		{`CONSTRAINT ck CHECK(label <> 'NOT VALID')`, false},
		{`CONSTRAINT ck CHECK(label <> 'NOT VALID') NOT VALID`, true},
		{`CONSTRAINT ck FOREIGN KEY(id) REFERENCES demo.parent(id) NOT VALID`, true},
		{`CONSTRAINT ck CHECK(label <> $$NOT VALID$$)`, false},
	} {
		actual, err := PostgreSQLConstraintUnvalidated(Clause{Kind: "constraint", Name: "ck", SQL: entry.sql}, "demo")
		if err != nil || actual != entry.unvalidated {
			t.Fatal(entry.sql, actual, err)
		}
	}
}
