package schemaddl

import (
	"reflect"
	"testing"
)

func TestPostgreSQLProbeTypesReadDeclarationsWithoutParsingOnServer(t *testing.T) {
	for _, sample := range []struct {
		raw  string
		want PostgreSQLProbeType
	}{
		{"INTEGER", PostgreSQLProbeType{Name: "int4"}},
		{"numeric(40,20)", PostgreSQLProbeType{Name: "numeric"}},
		{"double precision", PostgreSQLProbeType{Name: "float8"}},
		{"CHARACTER VARYING(100)", PostgreSQLProbeType{Name: "varchar"}},
		{"bit varying(9)", PostgreSQLProbeType{Name: "varbit"}},
		{"timestamp(6) with time zone", PostgreSQLProbeType{Name: "timestamptz"}},
		{"time without time zone", PostgreSQLProbeType{Name: "time"}},
		{"interval day to second(3)", PostgreSQLProbeType{Name: "interval"}},
		{"text[3][]", PostgreSQLProbeType{Name: "text", Array: true}},
		{"integer ARRAY[9]", PostgreSQLProbeType{Name: "int4", Array: true}},
		{`"Case"."Type"(7)[]`, PostgreSQLProbeType{Schema: "Case", Name: "Type", Array: true}},
		{`pg_catalog."char"`, PostgreSQLProbeType{Schema: "pg_catalog", Name: "char"}},
		{`"integer"`, PostgreSQLProbeType{Name: "integer"}},
	} {
		t.Run(sample.raw, func(t *testing.T) {
			draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: sample.raw}}}
			before := Signature(draft)
			got, err := PostgreSQLProbeTypes(draft, "s")
			if err != nil || !reflect.DeepEqual(got, []PostgreSQLProbeType{sample.want}) {
				t.Fatalf("type identity differs: %#v %v", got, err)
			}
			if Signature(draft) != before {
				t.Fatal("type capture changed the draft")
			}
		})
	}
}

func TestPostgreSQLProbeTypesCaptureNestedCastsAndTypedLiteralsInAllScopes(t *testing.T) {
	draft := Table{Name: "t", Clauses: []Clause{
		{ID: "new:x", Kind: "column", Name: "x", SQL: `integer DEFAULT CAST(CAST('1' AS s.source) AS integer)`},
		{ID: "new:y", Kind: "column", Name: "y", SQL: `text GENERATED ALWAYS AS (CAST(x AS "Case"."Target")) STORED`},
		{ID: "new:c", Kind: "constraint", Name: "c", SQL: `CONSTRAINT c CHECK(CASE WHEN x>0 THEN 'fake::hidden;foo()'::text ELSE s.domain '2'::text END <> '')`},
	}, Definitions: []Definition{
		{Kind: "index", Name: "i", SQL: `CREATE INDEX i ON s.t ((x::s.index_type)) WHERE x::integer>0`},
		{Kind: "trigger", Name: "tr", SQL: `CREATE TRIGGER tr BEFORE UPDATE ON s.t FOR EACH ROW WHEN (NEW.x::s.trigger_type>0) EXECUTE FUNCTION s.callback()`},
	}}
	got, err := PostgreSQLProbeTypes(draft, "s")
	want := []PostgreSQLProbeType{{Name: "int4"}, {Name: "int8"}, {Name: "numeric"}, {Name: "text"}, {Schema: "Case", Name: "Target"}, {Schema: "s", Name: "domain"}, {Schema: "s", Name: "index_type"}, {Schema: "s", Name: "source"}, {Schema: "s", Name: "trigger_type"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cast scopes differ: %#v %v", got, err)
	}
}

func TestPostgreSQLProbeTypesIgnoreStringsAndGrammarAndCheckNestedOperands(t *testing.T) {
	for _, expression := range []string{
		`CASE WHEN true THEN 'value' ELSE 'value' END`,
		`coalesce($q$ CAST(1 AS fake.type)::another $q$, 'value')`,
		`/* ::fake.type /* nested */ */ E'fake\\quote::hidden'`,
	} {
		draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: "text DEFAULT " + expression}}}
		got, err := PostgreSQLProbeTypes(draft, "s")
		want := []PostgreSQLProbeType{{Name: "text"}}
		if expression == `CASE WHEN true THEN 'value' ELSE 'value' END` {
			want = []PostgreSQLProbeType{{Name: "bool"}, {Name: "text"}}
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("grammar/literal read as type: %#v %v", got, err)
		}
	}
	draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: `integer DEFAULT CAST(('1'::s.nested)::integer AS integer)`}}}
	got, err := PostgreSQLProbeTypes(draft, "s")
	if err != nil || !reflect.DeepEqual(got, []PostgreSQLProbeType{{Name: "int4"}, {Name: "text"}, {Schema: "s", Name: "nested"}}) {
		t.Fatalf("nested casts escaped capture: %#v %v", got, err)
	}
}

func TestPostgreSQLProbeTypesRejectAmbiguousAndUnboundDeclarations(t *testing.T) {
	for _, raw := range []string{"a.b.c", "integer(unsafe())", "integer[unsafe]", "text; SELECT 1", "integer DEFAULT CAST('1' AS a.b.c)", "integer DEFAULT CAST('1' AS)", "integer DEFAULT CAST(1)", "integer DEFAULT 1::a.b.c"} {
		draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: raw}}}
		if _, err := PostgreSQLProbeTypes(draft, "s"); err == nil {
			t.Fatal("ambiguous type declaration accepted", raw)
		}
	}
	draft := Table{Name: "t", Definitions: []Definition{{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON other.t (x::integer)"}}}
	if _, err := PostgreSQLProbeTypes(draft, "s"); err == nil {
		t.Fatal("type capture accepted another table")
	}
}

func TestPostgreSQLProbeTypesNumericLiteralSourcesAndPrefixes(t *testing.T) {
	for _, sample := range []struct {
		raw  string
		want []PostgreSQLProbeType
	}{
		{`uuid DEFAULT CAST(1.25 AS uuid)`, []PostgreSQLProbeType{{Name: "int4"}, {Name: "int8"}, {Name: "numeric"}, {Name: "uuid"}}},
		{`uuid DEFAULT CAST('1.25' AS uuid)`, []PostgreSQLProbeType{{Name: "text"}, {Name: "uuid"}}},
		{`bit DEFAULT B'0101'`, []PostgreSQLProbeType{{Name: "bit"}, {Name: "text"}}},
		{`bit DEFAULT X'ff'`, []PostgreSQLProbeType{{Name: "bit"}, {Name: "text"}}},
		{`text DEFAULT N'fake::hidden'`, []PostgreSQLProbeType{{Name: "text"}}},
		{`text DEFAULT B '1'`, []PostgreSQLProbeType{{Name: "b"}, {Name: "text"}}},
		{`text DEFAULT "B" '1'`, []PostgreSQLProbeType{{Name: "B"}, {Name: "text"}}},
		{`uuid DEFAULT CAST(TIME '12:34' AS uuid)`, []PostgreSQLProbeType{{Name: "text"}, {Name: "time"}, {Name: "uuid"}}},
		{`timestamp DEFAULT TIMESTAMP '2000-01-01' AT TIME ZONE 'UTC'`, []PostgreSQLProbeType{{Name: "text"}, {Name: "timestamp"}}},
	} {
		draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: sample.raw}}}
		got, err := PostgreSQLProbeTypes(draft, "s")
		if err != nil || !reflect.DeepEqual(got, sample.want) {
			t.Fatalf("literal source/prefix differs for %s: %#v %v", sample.raw, got, err)
		}
	}
}
