package engines

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// This test harness executes native preview statements on its disposable server.
// It is not the production apply path: general value/dependency verification
// and engine-aware forms still have to be bound before exposing Apply.
func pgNativeFixtureApply(t *testing.T, s *Session, base contract.SchemaDesignSnapshot, draft schemaddl.Table, allowDrop bool) contract.SchemaDesignSnapshot {
	t.Helper()
	return pgNativeFixtureApplyChecked(t, s, base, draft, allowDrop)
}

func pgNativeFixtureApplyWithOriginalContext(t *testing.T, s *Session, base contract.SchemaDesignSnapshot, draft schemaddl.Table, allowDrop bool) contract.SchemaDesignSnapshot {
	t.Helper()
	return pgNativeFixtureApplyChecked(t, s, base, draft, allowDrop)
}

func pgNativeFixtureApplyChecked(t *testing.T, s *Session, base contract.SchemaDesignSnapshot, draft schemaddl.Table, allowDrop bool) contract.SchemaDesignSnapshot {
	t.Helper()
	plan, err := schemaddl.PlanPostgreSQL(base.Schema, draft, base.Target.Object.Schema, allowDrop)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, q := range []string{"SET LOCAL search_path=pg_catalog", "SET LOCAL standard_conforming_strings=on", "SET LOCAL row_security=off"} {
		if _, err = c.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if base.Schema.Exists {
		if _, err = c.ExecContext(context.Background(), "LOCK TABLE ONLY "+qualified(base.Target.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := postgresCatalogSnapshot(context.Background(), c, base.Target)
	if err != nil || actual.Schema.Revision != base.Schema.Revision {
		t.Fatal("native fixture scope changed before plan", err)
	}
	sequences, err := postgresSequenceStates(context.Background(), c, actual.Schema.Postgres)
	if err != nil {
		t.Fatal("original native sequence state unavailable", err)
	}
	removedSequences, err := postgresSequenceRemovals(base.Schema, draft, base.Target.Object.Schema, allowDrop)
	if err != nil {
		t.Fatal("native sequence removal permission unavailable", err)
	}
	var expected *postgresDesiredBaseline
	var beforeReferences []schemaddl.PostgreSQLReferenceBaseline
	verificationContext := base.Schema.Postgres
	if plan.Mode == "create" || plan.Mode == "alter" {
		canonical, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft, AllowColumnDrop: allowDrop})
		if err != nil {
			t.Fatal("independent desired definition unavailable", err)
		}
		expected = &canonical
		if base.Schema.Exists {
			beforeWrite, err := postgresCatalogSnapshotPreserving(context.Background(), c, base.Target, canonical.Probe.Postgres)
			if err != nil {
				t.Fatal("new reference before-images unavailable", err)
			}
			reference, err := schemaddl.CapturePostgreSQLReferenceBaseline(base.Schema, draft, canonical.Probe, beforeWrite.Schema)
			if err != nil {
				t.Fatal("new reference before-images are not independent", err)
			}
			beforeReferences = []schemaddl.PostgreSQLReferenceBaseline{reference}
			verificationContext = beforeWrite.Schema.Postgres.Clone()
		}
		probeSequences, err := postgresSequenceStates(context.Background(), c, actual.Schema.Postgres)
		if err != nil || postgresCompareSequenceStates(sequences, probeSequences, nil) != nil {
			t.Fatal("definition probe changed original sequence state", err)
		}
	}
	beforeNames, afterNames, err := postgresRetainedValues(base.Schema.Table, draft)
	if err != nil {
		t.Fatal(err)
	}
	var before postgresDesignDigest
	if base.Schema.Exists {
		before, err = postgresValueDigest(context.Background(), c, base.Target.Object, beforeNames, actual.Schema.Postgres)
		if err != nil {
			t.Fatal("original native values could not be verified", err)
		}
	}
	for _, q := range plan.SQL {
		if _, err = c.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("native statement failed: %s: %v", q, err)
		}
	}
	target := base.Target
	target.Object.Name = draft.Name
	actual, err = postgresCatalogSnapshot(context.Background(), c, target)
	if err != nil || !actual.Schema.Exists {
		t.Fatal("native fixture result unavailable", err)
	}
	if expected != nil {
		if err := schemaddl.VerifyPostgreSQLShape(actual.Schema.Table, expected.Table, target.Object.Schema); err != nil {
			t.Fatalf("native result differs from independent definition: %v\nactual: %#v\nexpected: %#v", err, actual.Schema.Table, expected.Table)
		}
		var references []schemaddl.PostgreSQLGraphReference
		if base.Schema.Exists {
			references = []schemaddl.PostgreSQLGraphReference{{Catalog: *base.Schema.Postgres, Names: pgDesiredGraphNames(base.Schema.Table)}}
		}
		got, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*actual.Schema.Postgres, pgDesiredGraphNames(expected.Table))
		if err != nil {
			t.Fatal("native target declaration projection unavailable", err)
		}
		want, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*expected.Probe.Postgres, expected.GraphNames, references...)
		if err != nil {
			t.Fatal("independent target declaration projection unavailable", err)
		}
		if err := schemaddl.ComparePostgreSQLDeclarationGraphs(got, want); err != nil {
			t.Fatal("native declaration graph differs from independent target", err)
		}
		if base.Schema.Exists {
			preservation, err := postgresCatalogSnapshotPreserving(context.Background(), c, target, verificationContext)
			if err != nil {
				t.Fatal("original catalog context unavailable after native change", err)
			}
			if err := schemaddl.VerifyPostgreSQLRetainedCatalog(base.Schema, draft, preservation.Schema, want, target.Object.Schema, allowDrop); err != nil {
				t.Fatal("native result lost retained original catalog properties", err)
			}
			storage, err := schemaddl.VerifyPostgreSQLStorage(base.Schema, draft, preservation.Schema, expected.Probe, target.Object.Schema, allowDrop, beforeReferences...)
			if err != nil {
				t.Fatal("native result changed unrequested physical storage", err)
			}
			if err := schemaddl.VerifyPostgreSQLUnselectedCatalog(base.Schema, draft, preservation.Schema, want, target.Object.Schema, allowDrop, storage); err != nil {
				t.Fatal("native result changed unrequested original context", err)
			}
			// Supplemental old context is verification-only. The returned
			// workbench baseline retains the ordinary current closure/revision.
		}
	}
	if plan.Mode == "rename_table" || plan.Mode == "rename_column" {
		if err := schemaddl.VerifyPostgreSQLRename(base.Schema, draft, actual.Schema, target.Object.Schema); err != nil {
			t.Fatal("native rename changed complete original catalog", err)
		}
	}
	after, err := postgresValueDigest(context.Background(), c, target.Object, afterNames, actual.Schema.Postgres)
	if err != nil {
		t.Fatal("changed native values could not be verified", err)
	}
	if before != after {
		t.Fatal("native change altered retained values/row count", schemaddl.ErrDataChanged)
	}
	sequenceCatalog, err := postgresCatalogSnapshotPreserving(context.Background(), c, target, base.Schema.Postgres)
	if err != nil {
		t.Fatal("native sequence catalog unavailable after change", err)
	}
	currentSequences, err := postgresSequenceStates(context.Background(), c, sequenceCatalog.Schema.Postgres)
	if err != nil {
		t.Fatal("native sequence state unavailable after change", err)
	}
	if err := postgresCompareSequenceStates(sequences, currentSequences, removedSequences); err != nil {
		t.Fatal("native plan changed unrequested sequence state", err)
	}
	if _, err = c.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	return actual
}

func pgNativeField(t *testing.T, table *schemaddl.Table, name string) *schemaddl.Clause {
	t.Helper()
	for i := range table.Clauses {
		if table.Clauses[i].Kind == "column" && table.Clauses[i].Name == name {
			return &table.Clauses[i]
		}
	}
	t.Fatal("field not in native draft", name)
	return nil
}

func pgNativeEditField(t *testing.T, table *schemaddl.Table, name string, edit func(*schemaddl.PostgreSQLField)) {
	t.Helper()
	c := pgNativeField(t, table, name)
	f, err := schemaddl.ParsePostgreSQLField(c.SQL)
	if err != nil {
		t.Fatal(err)
	}
	edit(&f)
	c.SQL, err = f.SQL()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL17DesignNativePlanFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_native_plan; CREATE FUNCTION nyadb_native_plan.on_change() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$; CREATE TABLE nyadb_native_plan.parent(id bigint PRIMARY KEY); INSERT INTO nyadb_native_plan.parent VALUES(1)`)
	base := pgDesignRead(t, s, "nyadb_native_plan", "native_table")
	draft := schemaddl.Table{Name: "native_table", Clauses: []schemaddl.Clause{
		{ID: "new:id", Kind: "column", Name: "id", SQL: `bigint GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME nyadb_native_plan.exact_sequence START WITH 9007199254740993 CACHE 13) NOT NULL`},
		{ID: "new:exact", Kind: "column", Name: "exact", SQL: `numeric(40,20) DEFAULT 1.00000000000000000001`},
		{ID: "new:label", Kind: "column", Name: "label", SQL: `text DEFAULT $text$semi;quote' NOT NULL$text$::text`},
		{ID: "new:nullable", Kind: "column", Name: "nullable", SQL: `text DEFAULT CASE WHEN false IS NOT NULL THEN 'nonnull'::text ELSE 'else'::text END`},
		{ID: "new:payload", Kind: "column", Name: "payload", SQL: `bytea`},
		{ID: "new:note", Kind: "column", Name: "note", SQL: `bigint DEFAULT 7`},
		{ID: "new:array", Kind: "column", Name: "array_values", SQL: `text[] DEFAULT ARRAY['first','second']::text[]`},
		{ID: "new:generated", Kind: "column", Name: "generated", SQL: `integer GENERATED ALWAYS AS (length(label)) STORED`},
		{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: `bigint DEFAULT 1`},
		{ID: "new:pk", Kind: "constraint", Name: "native_pk", SQL: `CONSTRAINT native_pk PRIMARY KEY(id)`},
		{ID: "new:check", Kind: "constraint", Name: "positive", SQL: `CONSTRAINT positive CHECK (exact>0)`},
		{ID: "new:fk", Kind: "constraint", Name: "native_fk", SQL: `CONSTRAINT native_fk FOREIGN KEY(parent_id) REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED`},
	}, Definitions: []schemaddl.Definition{
		{Kind: "index", Name: "native_idx", SQL: `CREATE INDEX native_idx ON nyadb_native_plan.native_table ((lower(label))) INCLUDE (payload) WHERE label IS NOT NULL`, Postgres: &schemaddl.PostgreSQLDefinitionProperties{Comment: "index comment"}},
		{Kind: "trigger", Name: "native_change", SQL: `CREATE TRIGGER native_change BEFORE UPDATE OF label,nullable ON nyadb_native_plan.native_table FOR EACH ROW EXECUTE FUNCTION nyadb_native_plan.on_change()`, Postgres: &schemaddl.PostgreSQLDefinitionProperties{Enabled: "R", Comment: "trigger comment"}},
	}, Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p", Parameters: []string{"fillfactor=75"}, Comment: "table comment", ReplicaIdentity: "f"}}
	base = pgNativeFixtureApply(t, s, base, draft, false)
	if len(base.Schema.Table.Columns()) != 9 || len(base.Schema.Table.Definitions) != 2 || base.Schema.Table.Definitions[1].Postgres.Enabled != "R" {
		t.Fatal("create lost native structure")
	}
	for _, c := range base.Schema.Table.Columns() {
		f, err := schemaddl.ParsePostgreSQLField(c.SQL)
		if err != nil || f.Unsupported {
			t.Fatal("real catalog column not understood", c.Name, c.SQL, err)
		}
		if _, err = f.SQL(); err != nil {
			t.Fatal("real catalog column did not render", c.Name, err)
		}
	}
	run(t, s, `INSERT INTO nyadb_native_plan.native_table(exact,payload) VALUES (12345678901234567890.12345678901234567890,decode('00ff','hex'))`)
	var originalExact, originalID, originalPayload, originalLabel, originalArray string
	if err := s.db.QueryRow(`SELECT exact::text,id::text,encode(payload,'hex'),label,array_values::text FROM nyadb_native_plan.native_table`).Scan(&originalExact, &originalID, &originalPayload, &originalLabel, &originalArray); err != nil {
		t.Fatal(err)
	}
	base = pgDesignRead(t, s, "nyadb_native_plan", "native_table")
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "exact", func(f *schemaddl.PostgreSQLField) { f.Type = "numeric(50,20)" })
	pgNativeEditField(t, &draft, "nullable", func(f *schemaddl.PostgreSQLField) {
		f.Type = "character varying(100)"
		f.Collation = `pg_catalog."C"`
		f.NotNull = true
	})
	pgNativeEditField(t, &draft, "label", func(f *schemaddl.PostgreSQLField) { f.Default = `'after;value'::text` })
	pgNativeField(t, &draft, "payload").Postgres.Storage = "e"
	pgNativeField(t, &draft, "payload").Postgres.Compression = "p"
	pgNativeField(t, &draft, "payload").Postgres.Statistics = 73
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 7"}, schemaddl.Clause{ID: "new:extra_check", Kind: "constraint", Name: "extra_check", SQL: `CONSTRAINT extra_check CHECK(extra>=0) NOT VALID`})
	draft.Definitions[0].SQL = `CREATE INDEX native_idx ON nyadb_native_plan.native_table ((lower(label))) INCLUDE(payload) WHERE label<>''`
	draft.Definitions[0].Postgres.Comment = "changed index"
	draft.Definitions[1].Postgres.Enabled = "D"
	draft.Postgres.Parameters = []string{"fillfactor=85", "autovacuum_enabled=false"}
	draft.Postgres.Comment = "changed table"
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var exact, id, payload, label, array string
	var extra int
	if err := s.db.QueryRow(`SELECT exact::text,id::text,encode(payload,'hex'),label,array_values::text,extra FROM nyadb_native_plan.native_table`).Scan(&exact, &id, &payload, &label, &array, &extra); err != nil {
		t.Fatal(err)
	}
	if exact != originalExact || id != originalID || payload != originalPayload || label != originalLabel || array != originalArray || extra != 7 {
		t.Fatal("native ALTER changed retained precise/typed values")
	}
	if pgNativeField(t, &base.Schema.Table, "payload").Postgres.Storage != "e" || pgNativeField(t, &base.Schema.Table, "payload").Postgres.Compression != "p" || base.Schema.Table.Definitions[1].Postgres.Enabled != "D" {
		t.Fatal("native attributes not applied")
	}
	if !strings.Contains(pgNativeField(t, &base.Schema.Table, "nullable").SQL, `COLLATE pg_catalog."C"`) {
		t.Fatal("explicit column collation missing")
	}
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "nullable", func(f *schemaddl.PostgreSQLField) { f.Collation = "" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	if strings.Contains(pgNativeField(t, &base.Schema.Table, "nullable").SQL, "COLLATE") {
		t.Fatal("native TYPE failed to restore the type default collation")
	}
	// Identity addition must establish NOT NULL before ADD GENERATED; mode
	// changes preserve the complete sequence settings and existing values.
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "note", func(f *schemaddl.PostgreSQLField) {
		f.HasDefault = false
		f.Default = ""
		f.Identity = "ALWAYS"
		f.IdentityOptions = "START WITH 100 CACHE 13"
		f.NotNull = true
	})
	base = pgNativeFixtureApply(t, s, base, draft, false)
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "note", func(f *schemaddl.PostgreSQLField) { f.Identity = "BY DEFAULT" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "note", func(f *schemaddl.PostgreSQLField) {
		f.Identity = ""
		f.IdentityOptions = ""
		f.HasDefault = true
		f.Default = "42"
		f.NotNull = false
	})
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var note int
	if err := s.db.QueryRow("SELECT note FROM nyadb_native_plan.native_table").Scan(&note); err != nil || note != 7 {
		t.Fatal("identity transitions changed retained values", err)
	}
	// PostgreSQL 17 supports replacing a stored generated expression natively.
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "generated", func(f *schemaddl.PostgreSQLField) { f.Generation = "length(label)*2" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var generated int
	if err := s.db.QueryRow("SELECT generated FROM nyadb_native_plan.native_table").Scan(&generated); err != nil || generated != len(originalLabel)*2 {
		t.Fatal("stored expression not replaced", err)
	}
	// Explicit removal only emits RESTRICT, not a table rebuild or CASCADE.
	draft = base.Schema.Table.Clone()
	var keep []schemaddl.Clause
	for _, c := range draft.Clauses {
		if c.Name != "extra" && c.Name != "extra_check" {
			keep = append(keep, c)
		}
	}
	draft.Clauses = keep
	base = pgNativeFixtureApply(t, s, base, draft, true)
	draft = base.Schema.Table.Clone()
	for i := range draft.Clauses {
		c := &draft.Clauses[i]
		if c.Kind == "constraint" && c.Name == "positive" {
			at := strings.Index(c.SQL, " CHECK")
			c.Name = "renamed_check"
			c.SQL = `CONSTRAINT "renamed_check"` + c.SQL[at:]
		}
	}
	base = pgNativeFixtureApply(t, s, base, draft, false)
	if !strings.Contains(base.Schema.DDL, "renamed_check") {
		t.Fatal("constraint rename failed")
	}
	run(t, s, `CREATE VIEW nyadb_native_plan.native_view AS SELECT label FROM nyadb_native_plan.native_table`)
	base = pgDesignRead(t, s, "nyadb_native_plan", "native_table")
	draft = base.Schema.Table.Clone()
	draft.Name = "renamed_table"
	base = pgNativeFixtureApply(t, s, base, draft, false)
	draft = base.Schema.Table.Clone()
	pgNativeField(t, &draft, "label").Name = "renamed_label"
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var viewValue string
	if err := s.db.QueryRow("SELECT label FROM nyadb_native_plan.native_view").Scan(&viewValue); err != nil || viewValue != originalLabel {
		t.Fatal("native rename broke catalog view dependency", err)
	}
	if !strings.Contains(base.Schema.DDL, "renamed_label") {
		t.Fatal("generated/index definitions did not follow native column rename")
	}
}
