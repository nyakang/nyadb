package schemaddl

import (
	"errors"
	"strings"
	"testing"
)

func TestPostgreSQLNativePlanKeepsTableAndStableMappings(t *testing.T) {
	base := pgMetadataFixture(t)
	draft := base.Table.Clone()
	draft.Clauses[0].SQL = "character varying(100) DEFAULT 'after;value' NOT NULL"
	draft.Clauses = append(draft.Clauses, Clause{ID: "new:1", Kind: "column", Name: "more", SQL: "numeric(40,20) DEFAULT 0"}, Clause{ID: "new:2", Kind: "constraint", Name: "positive", SQL: `CONSTRAINT "positive" CHECK (more>=0) NOT VALID`})
	draft.Definitions = []Definition{{Kind: "index", Name: "more_idx", SQL: `CREATE INDEX more_idx ON "demo"."quoted""table" (more) INCLUDE ("text""column") WHERE more>0`}}
	draft.Postgres.Comment = "changed"
	draft.Clauses[0].Postgres.Statistics = 73
	plan, err := PlanPostgreSQL(base, draft, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.SQL, ";\n")
	for _, part := range []string{`TYPE character varying(100)`, `USING "text""column"::character varying(100)`, `SET DEFAULT 'after;value'`, `SET NOT NULL`, `ADD COLUMN "more"`, `ADD CONSTRAINT "positive"`, `CREATE INDEX "more_idx" ON "demo"."quoted""table"`, `SET STATISTICS 73`, `COMMENT ON TABLE`} {
		if !strings.Contains(joined, part) {
			t.Fatalf("missing native step %s: %s", part, joined)
		}
	}
	if plan.Mode != "alter" || len(plan.Mappings) != 1 || plan.Mappings[0].ID != "column:1" || strings.Contains(joined, "DROP TABLE") || strings.Contains(joined, "CASCADE") {
		t.Fatal("native ALTER became a rebuild", plan)
	}
	if base.Table.Clauses[0].SQL != "text" || base.Table.Postgres.Comment != "before" {
		t.Fatal("planner mutated baseline")
	}
}

func TestPostgreSQLNativeTypeReappliesUnchangedColumnAttributes(t *testing.T) {
	base := pgMetadataFixture(t)
	for i := range base.Postgres.Entries {
		entry := &base.Postgres.Entries[i]
		if entry.Kind == "column" {
			*entry = pgRecord("column", 16, 1, `{"attnum":1,"attname":"text\"column","type_sql":"text","attstorage":"e","attcompression":"p","attstattarget":73,"attislocal":true,"comment":"preserved","attacl":["reader=r/owner"],"attoptions":["n_distinct=0.25"]}`)
		}
	}
	var err error
	base.Table, err = base.Postgres.Table()
	if err != nil {
		t.Fatal(err)
	}
	base.Revision, err = base.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	draft := base.Table.Clone()
	draft.Clauses[0].SQL = "character varying(100)"
	plan, err := PlanPostgreSQL(base, draft, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.SQL, "\n")
	for _, fragment := range []string{"SET STORAGE EXTERNAL", "SET COMPRESSION pglz", "SET STATISTICS 73"} {
		if !strings.Contains(joined, fragment) || strings.Index(joined, fragment) < strings.Index(joined, " TYPE ") {
			t.Fatal("TYPE did not restore retained attribute", fragment, joined)
		}
	}
	if len(draft.Clauses[0].Postgres.ACL) != 1 || len(draft.Clauses[0].Postgres.Parameters) != 1 {
		t.Fatal("planning altered retained opaque properties")
	}
}

func TestPostgreSQLNativeIdentityTypeRestoresExactBoundsWithoutRestart(t *testing.T) {
	before, err := ParsePostgreSQLField(`smallint GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME demo.original_seq INCREMENT BY 1 START WITH 100 MINVALUE 1 MAXVALUE 32767 CACHE 13 NO CYCLE) NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Type = "bigint"
	sql, err := pgAlterField(`"demo"."original"`, "id", before, after, 170000)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(sql, "\n")
	if !strings.Contains(joined, `ALTER SEQUENCE "demo"."original_seq" INCREMENT BY 1 START WITH 100 MINVALUE 1 MAXVALUE 32767 CACHE 13 NO CYCLE`) || strings.Contains(joined, "RESTART") {
		t.Fatal("type change reset identity allocation or lost bounds", joined)
	}
	before.IdentityOptions = "START WITH 100 CACHE 13"
	after.IdentityOptions = before.IdentityOptions
	if _, err := pgAlterField(`"demo"."original"`, "id", before, after, 170000); err == nil {
		t.Fatal("identity type change guessed missing sequence identity")
	}
}

func TestPostgreSQLNativePlanRequiresExplicitLossAndRejectsReordering(t *testing.T) {
	base := pgMetadataFixture(t)
	draft := base.Table.Clone()
	draft.Clauses = []Clause{{ID: "new:1", Kind: "column", Name: "replacement", SQL: "text"}}
	if _, err := PlanPostgreSQL(base, draft, "demo", false); err == nil {
		t.Fatal("column data loss not confirmed")
	}
	plan, err := PlanPostgreSQL(base, draft, "demo", true)
	if err != nil || len(plan.Dropped) != 1 || !strings.Contains(strings.Join(plan.SQL, ";"), `DROP COLUMN "text""column" RESTRICT`) {
		t.Fatal(plan, err)
	}
	draft = base.Table.Clone()
	draft.Clauses = append([]Clause{{ID: "new:1", Kind: "column", Name: "first", SQL: "text"}}, draft.Clauses...)
	if _, err := PlanPostgreSQL(base, draft, "demo", true); err == nil {
		t.Fatal("native ALTER promised unsupported column ordering")
	}
	draft = base.Table.Clone()
	draft.Clauses[0].ID = "spoofed"
	if _, err := PlanPostgreSQL(base, draft, "demo", false); err == nil {
		t.Fatal("same-name replacement borrowed old stable identity")
	}
}

func TestPostgreSQLNativeRenameStaysSeparateFromOtherEdits(t *testing.T) {
	base := pgMetadataFixture(t)
	draft := base.Table.Clone()
	draft.Name = "renamed"
	plan, err := PlanPostgreSQL(base, draft, "demo", false)
	if err != nil || plan.Mode != "rename_table" || len(plan.SQL) != 1 {
		t.Fatal(plan, err)
	}
	draft.Postgres.Comment = "other"
	if _, err = PlanPostgreSQL(base, draft, "demo", false); err == nil {
		t.Fatal("table rename combined with another draft")
	}
	draft = base.Table.Clone()
	draft.Clauses[0].Name = "renamed"
	plan, err = PlanPostgreSQL(base, draft, "demo", false)
	if err != nil || plan.Mode != "rename_column" || plan.RenameFrom != base.Table.Clauses[0].Name {
		t.Fatal(plan, err)
	}
	draft.Clauses[0].SQL = "integer"
	if _, err = PlanPostgreSQL(base, draft, "demo", false); err == nil {
		t.Fatal("column rename changed retained content simultaneously")
	}
}

func TestPostgreSQLDefinitionTargetsAndTransactionForms(t *testing.T) {
	for _, d := range []Definition{
		{Kind: "index", Name: "i", SQL: `CREATE UNIQUE INDEX i ON ONLY t USING btree ((lower(value))) INCLUDE (other) WHERE value IS NOT NULL;`},
		{Kind: "trigger", Name: "t", SQL: `CREATE TRIGGER t BEFORE UPDATE OF value,other ON "demo"."target" FOR EACH ROW EXECUTE FUNCTION demo.fn('semi;value')`},
		{Kind: "trigger", Name: "t", SQL: `CREATE CONSTRAINT TRIGGER t AFTER UPDATE ON target DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION demo.fn()`},
	} {
		target := "target"
		if d.Kind == "index" {
			target = "t"
		}
		sql, err := DefinitionPostgreSQL(d, "demo", target)
		if err != nil || !strings.Contains(sql, `"demo"."`+target+`"`) {
			t.Fatal(sql, err)
		}
	}
	for _, raw := range []string{
		`CREATE INDEX CONCURRENTLY i ON target(value)`, `CREATE INDEX IF NOT EXISTS i ON target(value)`,
		`CREATE INDEX other ON target(value)`, `CREATE INDEX demo.i ON target(value)`,
		`CREATE INDEX i ON other(value)`, `CREATE INDEX i ON external.target(value)`,
		`CREATE INDEX i ON target(value); DROP TABLE other`,
	} {
		if _, err := DefinitionPostgreSQL(Definition{Kind: "index", Name: "i", SQL: raw}, "demo", "target"); err == nil {
			t.Fatal("invalid index acquired execution scope", raw)
		}
	}
	if _, err := DefinitionPostgreSQL(Definition{Kind: "trigger", Name: "t", SQL: `CREATE OR REPLACE TRIGGER t BEFORE UPDATE ON target FOR EACH ROW EXECUTE FUNCTION demo.fn()`}, "demo", "target"); err == nil {
		t.Fatal("replacement trigger acquired new identity permission")
	}
}

func TestPostgreSQLCreatePlanUsesCapturedNamespaceAndCompleteDefinitions(t *testing.T) {
	base := pgMetadataFixture(t)
	base.Exists = false
	base.Postgres.RelationOID = 0
	base.Table = Table{Name: "new"}
	var err error
	base.Revision, err = base.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	draft := Table{Name: "new", Clauses: []Clause{
		{ID: "new:1", Kind: "column", Name: "id", SQL: `bigint GENERATED ALWAYS AS IDENTITY (START WITH 9007199254740993 CACHE 13) NOT NULL`},
		{ID: "new:2", Kind: "column", Name: "value", SQL: `text DEFAULT $sql$semi;quote$sql$::text`, Postgres: &PostgreSQLColumnProperties{Comment: "column", Statistics: -1}},
		{ID: "new:3", Kind: "constraint", Name: "pk", SQL: `CONSTRAINT pk PRIMARY KEY (id)`},
	}, Postgres: &PostgreSQLTableProperties{Persistence: "u", Parameters: []string{"fillfactor=75"}, Comment: "table", ReplicaIdentity: "f"}}
	plan, err := PlanPostgreSQL(base, draft, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.SQL, ";\n")
	for _, part := range []string{`CREATE UNLOGGED TABLE "demo"."new"`, `START WITH 9007199254740993 CACHE 13`, `CONSTRAINT "pk" PRIMARY KEY (id)`, `WITH (fillfactor=75`, `COMMENT ON COLUMN`, `REPLICA IDENTITY FULL`} {
		if !strings.Contains(joined, part) {
			t.Fatal("create lost definition", part, joined)
		}
	}
	if _, err = PlanPostgreSQL(base, draft, "other", false); err == nil {
		t.Fatal("cross-namespace plan accepted")
	}
	bad := base.Clone()
	bad.Revision = "stale"
	if _, err = PlanPostgreSQL(bad, draft, "demo", false); !errors.Is(err, ErrConflict) {
		t.Fatal("invalid original revision accepted", err)
	}
}

func TestPostgreSQLTypeChangeRecreatesUserTriggersWithCompleteProperties(t *testing.T) {
	base := pgMetadataFixture(t)
	base.Postgres.Entries = append(base.Postgres.Entries, pgRecord("trigger", 45, 0,
		`{"relation_oid":16,"name":"on_change","definition":"CREATE TRIGGER on_change BEFORE UPDATE OF \"text\"\"column\" ON demo.\"quoted\"\"table\" FOR EACH ROW EXECUTE FUNCTION demo.fn()","internal":false,"enabled":"R","comment":"preserved trigger"}`))
	var err error
	base.Table, err = base.Postgres.Table()
	if err != nil {
		t.Fatal(err)
	}
	base.Revision, err = base.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	draft := base.Table.Clone()
	draft.Clauses[0].SQL = "character varying(100)"
	plan, err := PlanPostgreSQL(base, draft, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.SQL, ";\n")
	drop, change, create := strings.Index(joined, `DROP TRIGGER "on_change"`), strings.Index(joined, " TYPE "), strings.Index(joined, `CREATE TRIGGER "on_change"`)
	if drop < 0 || change < drop || create < change || !strings.Contains(joined, `ENABLE REPLICA TRIGGER "on_change"`) || !strings.Contains(joined, "IS 'preserved trigger'") {
		t.Fatal("type change lost trigger order/state/comment", joined)
	}
}

func TestPostgreSQLForeignKeyQualifiesTheCapturedNamespace(t *testing.T) {
	for _, entry := range []struct{ raw, expected string }{
		{`CONSTRAINT fk FOREIGN KEY(parent_id) REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED`, `REFERENCES "demo"."parent"`},
		{`CONSTRAINT fk FOREIGN KEY(parent_id) REFERENCES "external"."Parent"(id) ON DELETE CASCADE`, `REFERENCES "external"."Parent"`},
	} {
		body, err := pgConstraintBody(Clause{Kind: "constraint", Name: "fk", SQL: entry.raw}, "demo")
		if err != nil || !strings.Contains(body, entry.expected) {
			t.Fatal("foreign key used physical search_path", body, err)
		}
	}
	if _, err := pgConstraintBody(Clause{Kind: "constraint", Name: "fk", SQL: `CONSTRAINT fk FOREIGN KEY(parent_id) REFERENCES other.demo.parent(id)`}, "demo"); err == nil {
		t.Fatal("cross-catalog reference entered plan")
	}
	if _, err := DefinitionPostgreSQL(Definition{Kind: "view", Name: "v", SQL: `CREATE VIEW v AS SELECT value FROM other ON something`}, "demo", "something"); err == nil {
		t.Fatal("non-design definition entered plan")
	}
}
