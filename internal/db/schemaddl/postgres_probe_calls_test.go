package schemaddl

import "testing"

func TestPostgreSQLIndexProbeCallsCaptureNestedAndQualifiedRoutines(t *testing.T) {
	for _, sample := range []struct {
		body string
		want []PostgreSQLProbeCall
	}{
		{`USING btree (value) INCLUDE(other) WITH(fillfactor=90) WHERE value IN(1,2)`, nil},
		{`((lower(value)),(LOWER(other)))`, []PostgreSQLProbeCall{{Name: "lower"}}},
		{`((pg_catalog.lower(value))) WHERE length(value)>0`, []PostgreSQLProbeCall{{Name: "length"}, {Schema: "pg_catalog", Name: "lower"}}},
		{`((COALESCE(lower(value),upper(other))))`, []PostgreSQLProbeCall{{Name: "lower"}, {Name: "upper"}}},
		{`(((value + (1)::numeric))) WHERE (value > (0)::numeric)`, nil},
		{`((evil.effect(lower(value))))`, []PostgreSQLProbeCall{{Name: "lower"}, {Schema: "evil", Name: "effect"}}},
		{`(("Evil"."Effect"('fake(foo())', $q$hidden.bar()$q$)))`, []PostgreSQLProbeCall{{Schema: "Evil", Name: "Effect"}}},
		{`(("coalesce"(value)))`, []PostgreSQLProbeCall{{Name: "coalesce"}}},
		{`((pg_catalog.coalesce(value)))`, []PostgreSQLProbeCall{{Schema: "pg_catalog", Name: "coalesce"}}},
		{`((/* fake.evil() /* nested.evil() */ */ lower(value)))`, []PostgreSQLProbeCall{{Name: "lower"}}},
		{`((value OPERATOR(pg_catalog.+) 1))`, nil},
		{`(value pg_catalog.int4_minmax_multi_ops(values_per_range=64))`, nil},
	} {
		d := Definition{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON s.t " + sample.body}
		before := Signature(d)
		calls, err := PostgreSQLIndexProbeCalls(d, "s", "t")
		if err != nil || len(calls) != len(sample.want) {
			t.Fatalf("calls differ: %v %v", calls, err)
		}
		for i := range calls {
			if calls[i] != sample.want[i] {
				t.Fatalf("routine identity differs: %v", calls)
			}
		}
		if Signature(d) != before {
			t.Fatal("call capture mutated a definition")
		}
	}
}

func TestPostgreSQLIndexProbeCallsRejectUnboundAndUnsupportedDispatch(t *testing.T) {
	for _, sql := range []string{
		`CREATE INDEX i ON other.t(value)`,
		`CREATE INDEX i ON s.t((a.b.c()))`,
		`CREATE INDEX i ON s.t(value OPERATOR(evil.+) 1)`, // Expression key needs parentheses.
		`CREATE INDEX i ON s.t((lower(value))); SELECT 1`,
		`CREATE INDEX i ON s.t`,
	} {
		if _, err := PostgreSQLIndexProbeCalls(Definition{Kind: "index", Name: "i", SQL: sql}, "s", "t"); err == nil {
			t.Fatal("unsupported/unbound index call capture succeeded")
		}
	}
}
