package schemaddl

import (
	"reflect"
	"testing"
)

func TestOpenGaussDesignReferencesIncludeSelfWithoutChangingPostgresLockScope(t *testing.T) {
	table := Table{Name: "original", Clauses: []Clause{
		{ID: "self", Kind: "constraint", Name: "self_fk", SQL: `CONSTRAINT self_fk FOREIGN KEY(id) REFERENCES original(id)`},
		{ID: "self_qualified", Kind: "constraint", Name: "quoted_fk", SQL: `CONSTRAINT quoted_fk FOREIGN KEY(id) REFERENCES "native"."original"(id)`},
		{ID: "external", Kind: "constraint", Name: "external_fk", SQL: `CONSTRAINT external_fk FOREIGN KEY(parent_id) REFERENCES other.parent(id)`},
		{ID: "literal", Kind: "constraint", Name: "check_text", SQL: `CONSTRAINT check_text CHECK(note<>'FOREIGN KEY REFERENCES untrusted.table')`},
	}}
	got, err := OpenGaussDesignReferencedTables(table, "native")
	want := []PostgreSQLTableReference{{Schema: "native", Name: "original"}, {Schema: "other", Name: "parent"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("native reference scope omitted/doubled self or parsed a literal", got, err)
	}
	got, err = PostgreSQLReferencedTables(table, "native")
	if err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatal("PostgreSQL external lock scope changed", got, err)
	}
}
