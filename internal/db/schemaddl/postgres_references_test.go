package schemaddl

import (
	"reflect"
	"testing"
)

func TestPostgreSQLReferencesUseQualifiedSortedForeignKeys(t *testing.T) {
	table := Table{Name: "t", Clauses: []Clause{
		{Kind: "constraint", Name: "one", SQL: `CONSTRAINT one FOREIGN KEY(id) REFERENCES "z"."Quoted Table"(id)`},
		{Kind: "constraint", Name: "two", SQL: `CONSTRAINT two FOREIGN KEY(id) REFERENCES parent(id)`},
		{Kind: "constraint", Name: "three", SQL: `CONSTRAINT three FOREIGN KEY(id) REFERENCES s.t(id)`},
		{Kind: "constraint", Name: "four", SQL: `CONSTRAINT four CHECK('REFERENCES secret(id)' IS NOT NULL)`},
	}}
	refs, err := PostgreSQLReferencedTables(table, "s")
	if err != nil || !reflect.DeepEqual(refs, []PostgreSQLTableReference{{"s", "parent"}, {"z", "Quoted Table"}}) {
		t.Fatal(refs, err)
	}
}

func TestPostgreSQLProbeRejectsHiddenQuerySources(t *testing.T) {
	for _, raw := range []string{`(SELECT value FROM evil.view)`, `EXISTS(SELECT 1)`, `sum(value) OVER ()`, `(TABLE evil.view)`} {
		table := Table{Name: "t", Clauses: []Clause{{ID: "new:c", Kind: "column", Name: "c", SQL: "integer DEFAULT " + raw}}}
		if _, err := PostgreSQLDeclarationProbeCalls(table, "s"); err == nil {
			t.Fatal("hidden query acquired parsing permission", raw)
		}
	}
}
