package engines

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignDeclarationFunctionEffectsFixture(t *testing.T) {
	for _, scope := range []string{"default", "generated", "check", "trigger_when"} {
		t.Run(scope, func(t *testing.T) {
			s := postgresDesignFixture(t)
			schema := "nyadb_declaration_effect_" + scope
			run(t, s, "CREATE SCHEMA "+quote(schema)+"; CREATE SEQUENCE "+quote(schema)+`.counter START WITH 41;
CREATE FUNCTION `+quote(schema)+`.effect() RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('`+schema+`.counter'); RETURN 1; END$$;
CREATE TABLE `+quote(schema)+`.original(value integer); INSERT INTO `+quote(schema)+`.original VALUES(7);`)
			if scope == "trigger_when" {
				run(t, s, "CREATE FUNCTION "+quote(schema)+".callback() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$")
			}
			r := pgDDLRequest(t, s, schema, "original")
			call := quote(schema) + ".effect()"
			switch scope {
			case "default":
				r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:effect", Kind: "column", Name: "effect", SQL: "integer DEFAULT " + call})
			case "generated":
				r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:effect", Kind: "column", Name: "effect", SQL: "integer GENERATED ALWAYS AS (" + call + ") STORED"})
			case "trigger_when":
				r.Desired.Definitions = append(r.Desired.Definitions, schemaddl.Definition{Kind: "trigger", Name: "effect_trigger", SQL: "CREATE TRIGGER effect_trigger BEFORE UPDATE ON " + quote(schema) + ".original FOR EACH ROW WHEN (" + call + ">0) EXECUTE FUNCTION " + quote(schema) + ".callback()"})
			case "check":
				r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:effect", Kind: "constraint", Name: "effect", SQL: "CONSTRAINT effect CHECK(" + call + ">0)"})
			}
			before := schemaddl.Signature(r)
			out := s.postgresDesignDDLApply(context.Background(), r)
			var called bool
			if err := s.db.QueryRowContext(context.Background(), "SELECT is_called FROM "+quote(schema)+".counter").Scan(&called); err != nil {
				t.Fatal(err)
			}
			if called {
				t.Error("declaration invoked nontransactional user function")
			}
			if !errors.Is(out.Error, errPostgresProbeRoutine) || out.Verified || out.Actual != nil || out.State != "rolled_back" {
				t.Error("user declaration function was not rejected by preflight", out.State, out.Error)
			}
			current := pgDDLRequest(t, s, schema, "original")
			if current.Original.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(r) != before {
				t.Error("declaration failure changed original catalog/request")
			}
		})
	}
}

// Real catalog checks also verify the parsed SQL wrapper bodies of round,
// trunc and substring. Common C/internal functions retain their original use.
func TestPostgreSQL17DesignDeclarationCoreRoutinesFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_declaration_core;
 CREATE TABLE nyadb_declaration_core.original(value numeric(40,20),label text);
 INSERT INTO nyadb_declaration_core.original VALUES(7.25,'abc');
 CREATE FUNCTION nyadb_declaration_core.callback() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;`)
	r := pgDDLRequest(t, s, "nyadb_declaration_core", "original")
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, sample := range []struct{ name, field string }{
		{"round", `numeric GENERATED ALWAYS AS (round(value)) STORED`},
		{"trunc", `numeric GENERATED ALWAYS AS (trunc(value)) STORED`},
		{"substring", `text DEFAULT substring('abc','b','!')`},
		{"encode", `text DEFAULT encode(decode('00ff','hex'),'hex')`},
		{"json", `text DEFAULT jsonb_typeof(jsonb_build_object('n',1))`},
		{"date", `timestamptz DEFAULT date_trunc('day',CURRENT_TIMESTAMP(3))`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			request := r.Clone()
			request.Desired.Clauses = append(request.Desired.Clauses, schemaddl.Clause{ID: "new:core", Kind: "column", Name: "core", SQL: sample.field})
			if _, err := s.postgresCanonicalDesired(context.Background(), c, request); err != nil {
				t.Fatal("supported core declaration rejected", err)
			}
			pgProbeAbsent(t, c, "nyadb_declaration_core")
		})
	}
	// Index-only calls use the same routine checker and bound SQL-body validation.
	for _, expression := range []string{`round(value)`, `trunc(value)`, `substring(label,'b','!')`, `round(value::numeric(40,20))`} {
		request := r.Clone()
		request.Desired.Definitions = append(request.Desired.Definitions, schemaddl.Definition{Kind: "index", Name: "core_index", SQL: "CREATE INDEX core_index ON nyadb_declaration_core.original ((" + expression + "))"})
		if _, err := s.postgresCanonicalDesired(context.Background(), c, request); err != nil {
			t.Fatal("supported core index rejected", expression, err)
		}
	}
	request := r.Clone()
	request.Desired.Definitions = append(request.Desired.Definitions, schemaddl.Definition{Kind: "trigger", Name: "core_trigger", SQL: `CREATE TRIGGER core_trigger BEFORE UPDATE ON nyadb_declaration_core.original FOR EACH ROW WHEN (length(NEW.label)>0) EXECUTE FUNCTION nyadb_declaration_core.callback()`})
	if _, err := s.postgresCanonicalDesired(context.Background(), c, request); err != nil {
		t.Fatal("trigger declaration treated as invocation", err)
	}
}

func TestPostgreSQL17DesignDeclarationSequenceDefaultsFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_declaration_sequence;
 CREATE SEQUENCE nyadb_declaration_sequence.counter START WITH 41;
 CREATE TABLE nyadb_declaration_sequence.original(value integer);
 INSERT INTO nyadb_declaration_sequence.original VALUES(7);
 CREATE TABLE nyadb_declaration_sequence.empty(value integer);
 CREATE TABLE nyadb_declaration_sequence.serial_asset(id serial,value integer);
 INSERT INTO nyadb_declaration_sequence.serial_asset(value) VALUES(7);`)
	request := func(table string) contract.SchemaDesignRequest {
		r := pgDDLRequest(t, s, "nyadb_declaration_sequence", table)
		r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:sequence", Kind: "column", Name: "next_id", SQL: `bigint DEFAULT nextval('nyadb_declaration_sequence.counter'::regclass)`})
		return r
	}
	r := request("original")
	before := schemaddl.Signature(r)
	out := s.postgresDesignDDLApply(context.Background(), r)
	if !errors.Is(out.Error, errPostgresSequenceDefault) || out.State != "rolled_back" || out.Verified || out.Actual != nil {
		t.Fatal("populating sequence default accepted", out)
	}
	if current := pgDDLRequest(t, s, "nyadb_declaration_sequence", "original"); current.Original.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(r) != before {
		t.Fatal("rejection changed catalog/request")
	}
	for _, table := range []string{"empty", "created"} {
		r = request(table)
		if table == "created" {
			r.Desired.Postgres = &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}
		}
		out = s.postgresDesignDDLApply(context.Background(), r)
		if out.Error != nil || out.State != "committed" || !out.Verified {
			t.Fatal("empty sequence declaration rejected", table, out)
		}
	}
	// A SET DEFAULT does not fill existing rows, including a retained serial
	// default, and must not be rejected as though it were a new column.
	r = pgDDLRequest(t, s, "nyadb_declaration_sequence", "original")
	pgNativeEditField(t, &r.Desired, "value", func(f *schemaddl.PostgreSQLField) {
		f.HasDefault = true
		f.Default = `nextval('nyadb_declaration_sequence.counter'::regclass)`
	})
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified {
		t.Fatal("SET DEFAULT rejected", out)
	}
	r = pgDDLRequest(t, s, "nyadb_declaration_sequence", "serial_asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "text"})
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	canonical, err := s.postgresCanonicalDesired(context.Background(), c, r)
	if err != nil {
		t.Fatal("serial default probe rejected", err)
	}
	expected, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*canonical.Probe.Postgres, canonical.GraphNames, schemaddl.PostgreSQLGraphReference{Catalog: *r.Original.Schema.Postgres, Names: postgresDesignGraphNames(r.Original.Schema.Table)})
	if err != nil {
		t.Fatal(err)
	}
	beforeExpected := schemaddl.Signature(expected)
	serialExpected, err := schemaddl.PostgreSQLRetainedSerialDeclaration(r.Original.Schema, r.Desired, expected)
	if err != nil || schemaddl.Signature(expected) != beforeExpected {
		t.Fatal("serial graph changed independent input", err)
	}
	if err := rollbackDesign(c); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified {
		t.Fatal("retained serial default rejected", out)
	}
	got, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*out.Actual.Schema.Postgres, postgresDesignGraphNames(r.Desired))
	if err != nil {
		t.Fatal(err)
	}
	if err := schemaddl.ComparePostgreSQLDeclarationGraphs(got, serialExpected); err != nil {
		t.Fatal("serial graph does not match original ownership", err)
	}
	// Evidence injection only: an altered sequence declaration cannot hide
	// behind its preserved default expression or a matching stable column ID.
	changed := got.Clone()
	injected := false
	for i, record := range changed.Records {
		if record.Kind != "sequence" {
			continue
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(record.Declaration, &data); err != nil {
			t.Fatal(err)
		}
		data["future_serial_property"] = json.RawMessage(`9007199254740993`)
		changed.Records[i].Declaration, err = json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		injected = true
	}
	if !injected || schemaddl.ComparePostgreSQLDeclarationGraphs(changed, serialExpected) == nil {
		t.Fatal("changed serial declaration acquired approval")
	}
	var called bool
	if err := s.db.QueryRow(`SELECT is_called FROM nyadb_declaration_sequence.counter`).Scan(&called); err != nil || called {
		t.Fatal("sequence default declaration consumed external value", err)
	}
	var id int
	if err := s.db.QueryRow(`SELECT last_value FROM nyadb_declaration_sequence.serial_asset_id_seq`).Scan(&id); err != nil || id != 1 {
		t.Fatal("retained serial sequence changed", err)
	}
}

func TestPostgreSQL17DesignDeclarationChangedCoreBodyFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_declaration_changed_core;
 CREATE SEQUENCE nyadb_declaration_changed_core.counter START WITH 41;
 CREATE FUNCTION nyadb_declaration_changed_core.effect() RETURNS numeric LANGUAGE plpgsql IMMUTABLE AS $$BEGIN PERFORM nextval('nyadb_declaration_changed_core.counter'); RETURN 1; END$$;
 CREATE TABLE nyadb_declaration_changed_core.original(value numeric); INSERT INTO nyadb_declaration_changed_core.original VALUES(7);`)
	var original string
	if err := s.db.QueryRow(`SELECT pg_catalog.pg_get_functiondef('pg_catalog.round(numeric)'::regprocedure)`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run(t, s, original) })
	run(t, s, `CREATE OR REPLACE FUNCTION pg_catalog.round(numeric) RETURNS numeric LANGUAGE SQL IMMUTABLE RETURN nyadb_declaration_changed_core.effect();`)
	r := pgDDLRequest(t, s, "nyadb_declaration_changed_core", "original")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:effect", Kind: "column", Name: "effect", SQL: "numeric DEFAULT round(1::numeric)"})
	out := s.postgresDesignDDLApply(context.Background(), r)
	if !errors.Is(out.Error, errPostgresProbeRoutine) || out.State != "rolled_back" || out.Verified {
		t.Fatal("core SQL wrapper authorized user function", out)
	}
	var called bool
	if err := s.db.QueryRow(`SELECT is_called FROM nyadb_declaration_changed_core.counter`).Scan(&called); err != nil || called {
		t.Fatal("changed core wrapper executed callback", err)
	}
}

func TestPostgreSQL17DesignDeclarationRoutineScopeFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := postgresExpressionRoutineCheck{ctx: context.Background(), c: c, checked: map[string]bool{}}
	for _, name := range []string{"jsonb_build_object", "now", "nextval", "random", "gen_random_uuid", "clock_timestamp"} {
		call := schemaddl.PostgreSQLProbeCall{Name: name}
		if err := p.call(call, "default"); err != nil {
			t.Fatal("supported default routine rejected", name, err)
		}
		if err := p.call(call, "generated"); !errors.Is(err, errPostgresProbeRoutine) {
			t.Fatal("default approval leaked into generated scope", name, err)
		}
	}
	for _, scope := range []string{"constraint", "trigger_when", "index"} {
		if err := p.call(schemaddl.PostgreSQLProbeCall{Name: "nextval"}, scope); !errors.Is(err, errPostgresProbeRoutine) {
			t.Fatal("sequence routine admitted to non-default scope", scope, err)
		}
	}
}
