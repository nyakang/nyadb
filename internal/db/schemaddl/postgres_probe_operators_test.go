package schemaddl

import (
	"reflect"
	"testing"
)

func TestPostgreSQLProbeOperatorsCaptureSymbolsAndComparisonGrammar(t *testing.T) {
	for _, sample := range []struct {
		expression string
		want       []PostgreSQLProbeOperator
	}{
		{`(1+2)*(3-4)/5`, []PostgreSQLProbeOperator{{Name: "*"}, {Name: "+"}, {Name: "-"}, {Name: "/"}}},
		{`x != 1 AND x<=3`, []PostgreSQLProbeOperator{{Name: "<="}, {Name: "<>"}}},
		{`x OPERATOR("Case".#@) 1`, []PostgreSQLProbeOperator{{Schema: "Case", Name: "#@"}}},
		{`x OPERATOR(pg_catalog.!=) 1`, []PostgreSQLProbeOperator{{Schema: "pg_catalog", Name: "<>"}}},
		{`x IN(1,2) OR x BETWEEN 3 AND 4`, []PostgreSQLProbeOperator{{Name: "<="}, {Name: "<>"}, {Name: "="}, {Name: ">="}}},
		{`x NOT LIKE 'fake+foo' OR x ILIKE $q$ hidden#@text $q$`, []PostgreSQLProbeOperator{{Name: "!~~"}, {Name: "~~*"}}},
		{`x IS DISTINCT FROM y OR NULLIF(x,y)>0`, []PostgreSQLProbeOperator{{Name: "="}, {Name: ">"}}},
		{`CASE x WHEN 1 THEN GREATEST(1,2) ELSE LEAST(3,4) END`, []PostgreSQLProbeOperator{{Name: "<"}, {Name: "="}, {Name: ">"}}},
		{`replace(string=>'fake+text',from=>'x',to=>'y')`, nil},
		{`x::integer /* fake #@ /* nested */ */ + +1`, []PostgreSQLProbeOperator{{Name: "+"}}},
	} {
		draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: "integer DEFAULT " + sample.expression}}}
		before := Signature(draft)
		got, err := PostgreSQLProbeOperators(draft, "s")
		if err != nil || len(got) != len(sample.want) {
			t.Fatalf("operator capture differs for %s: %#v %v", sample.expression, got, err)
		}
		for i := range got {
			if got[i] != sample.want[i] {
				t.Fatalf("dispatch identity differs: %#v", got)
			}
		}
		if Signature(draft) != before {
			t.Fatal("operator capture mutated draft")
		}
	}
}

func TestPostgreSQLProbeOperatorsCaptureEachDeclarationScope(t *testing.T) {
	draft := Table{Name: "t", Clauses: []Clause{
		{ID: "new:x", Kind: "column", Name: "x", SQL: "integer GENERATED ALWAYS AS (1+2) STORED"},
		{ID: "new:c", Kind: "constraint", Name: "c", SQL: "CONSTRAINT c CHECK(x>=0)"},
	}, Definitions: []Definition{
		{Kind: "index", Name: "i", SQL: `CREATE INDEX i ON s.t ((x-1)) WITH(fillfactor=90) WHERE x<3`},
		{Kind: "trigger", Name: "tr", SQL: `CREATE TRIGGER tr BEFORE UPDATE ON s.t FOR EACH ROW WHEN (NEW.x>0) EXECUTE FUNCTION s.callback()`},
	}}
	got, err := PostgreSQLProbeOperators(draft, "s")
	want := []PostgreSQLProbeOperator{{Name: "+"}, {Name: "-"}, {Name: "<"}, {Name: ">"}, {Name: ">="}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("declaration scopes differ: %#v %v", got, err)
	}
}

func TestPostgreSQLIndexProbeLayoutCapturesClassAndKeepsExpressionsUnknown(t *testing.T) {
	d := Definition{Kind: "index", Name: "i", SQL: `CREATE INDEX i ON s.t USING brin ("Case" pg_catalog.int4_minmax_multi_ops(values_per_range=64), (lower(label)) COLLATE pg_catalog."C" "Types"."Class" DESC NULLS FIRST) INCLUDE(other) WITH(pages_per_range=128) WHERE label<>''`}
	before := Signature(d)
	got, err := PostgreSQLIndexProbeLayout(d, "s", "t")
	want := PostgreSQLProbeIndexLayout{Method: "brin", Keys: []PostgreSQLProbeIndexKey{{Column: "Case", Class: PostgreSQLProbeClass{Schema: "pg_catalog", Name: "int4_minmax_multi_ops"}}, {Class: PostgreSQLProbeClass{Schema: "Types", Name: "Class"}}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("index dispatch differs: %#v %v", got, err)
	}
	if Signature(d) != before {
		t.Fatal("index layout mutated definition")
	}
	for _, body := range []string{`(value)`, `(lower(value))`, `((value+1))`, `(pg_catalog.lower(value))`} {
		d.SQL = "CREATE INDEX i ON s.t " + body
		if got, err := PostgreSQLIndexProbeLayout(d, "s", "t"); err != nil || got.Method != "btree" || len(got.Keys) != 1 {
			t.Fatal("supported index key rejected", err)
		}
	}
}

func TestPostgreSQLProbeDispatchRejectsAmbiguousAndUnboundGrammar(t *testing.T) {
	for _, body := range []string{`(value + 1)`, `(value ns.extra.class)`, `(value ASC class)`, `(value NULLS)`, `(value,)`, `USING ns.method(value)`, `(value OPERATOR(ns.+) 1)`} {
		if _, err := PostgreSQLIndexProbeLayout(Definition{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON s.t " + body}, "s", "t"); err == nil {
			t.Fatal("ambiguous index dispatch accepted", body)
		}
	}
	for _, expression := range []string{`x OPERATOR(a.b.+) 1`, `x OPERATOR(s."+") 1`, `x OPERATOR(s.+ +) 1`, `x OPERATOR(=>) 1`} {
		draft := Table{Name: "t", Clauses: []Clause{{ID: "new:x", Kind: "column", Name: "x", SQL: "integer DEFAULT " + expression}}}
		if _, err := PostgreSQLProbeOperators(draft, "s"); err == nil {
			t.Fatal("ambiguous operator identity accepted", expression)
		}
	}
	draft := Table{Name: "t", Definitions: []Definition{{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON other.t (x)"}}}
	if _, err := PostgreSQLProbeOperators(draft, "s"); err == nil {
		t.Fatal("operator capture accepted another table")
	}
}
