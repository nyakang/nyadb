package schemaddl

import (
	"strings"
	"testing"
)

func TestOpenGaussReferenceAnchorOmitsExecutableExpressionsAndBindsOneParent(t *testing.T) {
	source := Table{Name: "child", Clauses: []Clause{
		{ID: "id", Kind: "column", Name: "id", SQL: "bigserial"},
		{ID: "parent", Kind: "column", Name: "parent_id", SQL: "bigint DEFAULT nextval('private_sequence')"},
		{ID: "generated", Kind: "column", Name: "derived", SQL: "bigint GENERATED ALWAYS AS(id+parent_id) STORED"},
		{ID: "fk", Kind: "constraint", Name: "fk", SQL: `CONSTRAINT fk FOREIGN KEY(parent_id) REFERENCES "other"."parent"(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED`},
		{ID: "other_fk", Kind: "constraint", Name: "other_fk", SQL: "CONSTRAINT other_fk FOREIGN KEY(id) REFERENCES public.another(id)"},
		{ID: "check", Kind: "constraint", Name: "check", SQL: "CONSTRAINT check CHECK(id>private_function())"},
	}}
	before := Signature(source)
	anchor, names, err := OpenGaussReferenceAnchor(source, "public", "private_anchor", PostgreSQLTableReference{"other", "parent"})
	if err != nil || len(anchor.Clauses) != 4 || len(names.Constraints) != 1 || before != Signature(source) {
		t.Fatal("reference anchor lost scope or changed source", anchor, err)
	}
	for _, column := range anchor.Columns() {
		if column.SQL != "bigint" {
			t.Fatal("reference lock retained executable column expressions", column)
		}
	}
	fk := anchor.Clauses[len(anchor.Clauses)-1]
	if names.Constraints[fk.Name] != "fk" || !strings.HasSuffix(fk.SQL, "NOT VALID") || !strings.Contains(fk.SQL, "ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED") {
		t.Fatal("anchor changed native reference/actions or validation scope", fk)
	}
}
