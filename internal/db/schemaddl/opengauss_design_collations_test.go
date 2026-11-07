package schemaddl

import "testing"

func TestOpenGaussDesignCollationsIncludeExpressionsAndPreserveQuotedIdentity(t *testing.T) {
	table := Table{Name: "t", Clauses: []Clause{
		{Kind: "column", Name: "label", SQL: `text COLLATE PG_CATALOG."C" DEFAULT 'COLLATE user.fake'`},
		{Kind: "constraint", Name: "check_label", SQL: `CONSTRAINT check_label CHECK(label COLLATE "Private"."Case" <> '')`},
	}, Definitions: []Definition{{Name: "by_label", Kind: "index", SQL: `CREATE INDEX by_label ON s.t (label COLLATE pg_catalog."C")`}}}
	refs, err := OpenGaussDesignCollations(table, "s")
	if err != nil || len(refs) != 2 || refs[0] != (PostgreSQLProbeClass{Schema: "Private", Name: "Case"}) || refs[1] != (PostgreSQLProbeClass{Schema: "pg_catalog", Name: "C"}) {
		t.Fatal("literal text or identifier casing changed collation scope", refs, err)
	}
	table.Clauses[0].SQL = "text COLLATE"
	if _, err = OpenGaussDesignCollations(table, "s"); err == nil {
		t.Fatal("incomplete collation scope was accepted")
	}
}
