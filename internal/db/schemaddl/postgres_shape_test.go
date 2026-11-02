package schemaddl

import "testing"

func pgShapeFixture() Table {
	return Table{Name: "target", Postgres: &PostgreSQLTableProperties{Kind: "r", Persistence: "p", Owner: "owner", Parameters: []string{"fillfactor=75", "autovacuum_enabled=false"}, ACL: []string{"reader=r/owner"}}, Clauses: []Clause{
		{ID: "column:1", Kind: "column", Name: "id", SQL: `bigint GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME demo.original_seq INCREMENT BY 1 START WITH 9007199254740993 MINVALUE 1 MAXVALUE 9223372036854775807 CACHE 13 NO CYCLE) NOT NULL`},
		{ID: "column:2", Kind: "column", Name: "value", SQL: `text DEFAULT 'keep;value'::text`, Postgres: &PostgreSQLColumnProperties{Storage: "e", Compression: "p", Statistics: 73, Comment: "preserved"}},
		{ID: "constraint:3", Kind: "constraint", Name: "ck", SQL: `CONSTRAINT ck CHECK(value <> 'NOT VALID') NOT VALID`},
	}, Definitions: []Definition{
		{Kind: "index", Name: "idx", SQL: `CREATE INDEX idx ON demo.target USING btree(value) WHERE value <> 'keep'::text`, Postgres: &PostgreSQLDefinitionProperties{Comment: "index"}},
		{Kind: "trigger", Name: "tr", SQL: `CREATE TRIGGER tr BEFORE UPDATE ON demo.target FOR EACH ROW EXECUTE FUNCTION demo.fn('target','tr')`, Postgres: &PostgreSQLDefinitionProperties{Enabled: "R", Comment: "trigger"}},
	}}
}

func TestPostgreSQLShapeAllowsCatalogIdentityAndReloptionOrder(t *testing.T) {
	expected := pgShapeFixture()
	actual := expected.Clone()
	actual.Clauses[0].ID = "column:900"
	actual.Clauses[2].ID = "constraint:900"
	actual.Postgres.Parameters[0], actual.Postgres.Parameters[1] = actual.Postgres.Parameters[1], actual.Postgres.Parameters[0]
	actual.Definitions[0].SQL = `CREATE INDEX "idx" ON "demo"."target" USING btree(value) WHERE value <> 'keep'::text`
	if err := VerifyPostgreSQLShape(actual, expected, "demo"); err != nil {
		t.Fatal(err)
	}
	if expected.Postgres.Parameters[0] != "fillfactor=75" {
		t.Fatal("verification mutated desired reloptions")
	}
}

func TestPostgreSQLShapeRejectsSemanticAndOpaqueChanges(t *testing.T) {
	expected := pgShapeFixture()
	for _, entry := range []struct {
		name   string
		change func(*Table)
	}{
		{"column order", func(x *Table) { x.Clauses[0], x.Clauses[1] = x.Clauses[1], x.Clauses[0] }},
		{"extra option", func(x *Table) { x.Options = "unexpected" }},
		{"acl", func(x *Table) { x.Postgres.ACL = nil }},
		{"column storage", func(x *Table) { x.Clauses[1].Postgres.Storage = "x" }},
		{"column literal", func(x *Table) { x.Clauses[1].SQL = `text DEFAULT 'different'::text` }},
		{"sequence identity", func(x *Table) {
			x.Clauses[0].SQL = `bigint GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME demo.replaced_seq INCREMENT BY 1 START WITH 9007199254740993 MINVALUE 1 MAXVALUE 9223372036854775807 CACHE 13 NO CYCLE) NOT NULL`
		}},
		{"constraint validation", func(x *Table) { x.Clauses[2].SQL = `CONSTRAINT ck CHECK(value <> 'NOT VALID')` }},
		{"index expression", func(x *Table) {
			x.Definitions[0].SQL = `CREATE INDEX idx ON demo.target USING btree(value) WHERE value <> 'changed'::text`
		}},
		{"trigger enabled", func(x *Table) { x.Definitions[1].Postgres.Enabled = "D" }},
		{"trigger literal", func(x *Table) {
			x.Definitions[1].SQL = `CREATE TRIGGER tr BEFORE UPDATE ON demo.target FOR EACH ROW EXECUTE FUNCTION demo.fn('another','tr')`
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			actual := expected.Clone()
			entry.change(&actual)
			if err := VerifyPostgreSQLShape(actual, expected, "demo"); err == nil {
				t.Fatal("changed result acquired verification")
			}
		})
	}
}
