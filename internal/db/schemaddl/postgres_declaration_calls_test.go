package schemaddl

import (
	"reflect"
	"testing"
)

func TestPostgreSQLDeclarationProbeCallsPreserveScopesAndSkipGrammar(t *testing.T) {
	table := Table{Name: "t", Clauses: []Clause{
		{ID: "c1", Kind: "column", Name: "amount", SQL: `numeric(40,20) DEFAULT CAST(evil.effect() AS numeric(40,20))`},
		{ID: "c2", Kind: "column", Name: "computed", SQL: `numeric GENERATED ALWAYS AS (round(amount::numeric(40,20))) STORED`},
		{ID: "c3", Kind: "constraint", Name: "ck", SQL: `CONSTRAINT ck CHECK(COALESCE("Evil"."Effect"(),lower('fake.call()')) IS NOT NULL)`},
		{ID: "c4", Kind: "constraint", Name: "fk", SQL: `CONSTRAINT fk FOREIGN KEY(amount) REFERENCES s.reference(amount)`},
		{ID: "c5", Kind: "constraint", Name: "pk", SQL: `CONSTRAINT pk PRIMARY KEY(amount)`},
		{ID: "c6", Kind: "constraint", Name: "ex", SQL: `CONSTRAINT ex EXCLUDE USING gist ((evil.effect()) WITH =) WHERE (lower('a') IS NOT NULL)`},
	}, Definitions: []Definition{
		{Kind: "index", Name: "i", SQL: `CREATE INDEX i ON s.t((pg_catalog.round(amount::numeric(40,20))))`},
		{Kind: "trigger", Name: "tr", SQL: `CREATE TRIGGER tr BEFORE UPDATE ON s.t FOR EACH ROW WHEN (evil.effect()>0) EXECUTE FUNCTION evil.callback('fake.hidden()')`},
	}}
	before := Signature(table)
	calls, err := PostgreSQLDeclarationProbeCalls(table, "s")
	want := []PostgreSQLDeclarationProbeCall{
		{PostgreSQLProbeCall{"evil", "effect"}, "default", "c1"},
		{PostgreSQLProbeCall{"", "round"}, "generated", "c2"},
		{PostgreSQLProbeCall{"", "lower"}, "constraint", "c3"},
		{PostgreSQLProbeCall{"Evil", "Effect"}, "constraint", "c3"},
		{PostgreSQLProbeCall{"", "lower"}, "constraint", "c6"},
		{PostgreSQLProbeCall{"evil", "effect"}, "constraint", "c6"},
		{PostgreSQLProbeCall{"pg_catalog", "round"}, "index", "i"},
		{PostgreSQLProbeCall{"evil", "effect"}, "trigger_when", "tr"},
	}
	if err != nil || !reflect.DeepEqual(calls, want) {
		t.Fatalf("scope or identity differs: %#v %v", calls, err)
	}
	if Signature(table) != before {
		t.Fatal("collection mutated table")
	}
}

func TestPostgreSQLExpressionProbeCallsDistinguishTypmodsAndNestedOperands(t *testing.T) {
	for _, sample := range []struct {
		raw  string
		want []PostgreSQLProbeCall
	}{
		{`numeric(40,20) '1.2'`, nil},
		{`(1)::pg_catalog.numeric(40,20)`, nil},
		{`CAST(CAST(evil.effect() AS numeric(40,20)) AS double precision)`, []PostgreSQLProbeCall{{"evil", "effect"}}},
		{`CURRENT_TIMESTAMP(3)`, nil}, {`CURRENT_TIME(3)`, nil}, {`LOCALTIME(3)`, nil}, {`LOCALTIMESTAMP(3)`, nil},
		{`"current_timestamp"(3)`, []PostgreSQLProbeCall{{Name: "current_timestamp"}}},
		{`pg_catalog.current_timestamp(3)`, []PostgreSQLProbeCall{{"pg_catalog", "current_timestamp"}}},
		{`CAST(COALESCE(evil.effect(),1) AS numeric(40,20)) + lower($s$fake()$s$)`, []PostgreSQLProbeCall{{Name: "lower"}, {"evil", "effect"}}},
	} {
		ts, err := postgresTokens(sample.raw, true)
		if err != nil {
			t.Fatal(err)
		}
		calls, err := pgExpressionProbeCalls(ts, 0, nil)
		if err != nil || len(calls) != len(sample.want) {
			t.Fatalf("%s: %v %v", sample.raw, calls, err)
		}
		for i := range calls {
			if calls[i] != sample.want[i] {
				t.Fatalf("%s: %v", sample.raw, calls)
			}
		}
	}
	for _, raw := range []string{`CAST(1 AS numeric(evil.effect()))`, `CURRENT_TIMESTAMP(evil.effect())`, `CAST(1)`, `a.b.c()`} {
		ts, err := postgresTokens(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgExpressionProbeCalls(ts, 0, nil); err == nil {
			t.Fatal("unsupported grammar acquired permission", raw)
		}
	}
}
