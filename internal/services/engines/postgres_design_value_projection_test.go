package engines

import (
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQLValueProofSeparatesIdentityAllocationAndGeneratedInputs(t *testing.T) {
	original := schemaddl.Table{Name: "t", Clauses: []schemaddl.Clause{{ID: "kept", Kind: "column", Name: "id", SQL: "integer GENERATED ALWAYS AS IDENTITY"}}}
	desired := original.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:identity", Kind: "column", Name: "allocated", SQL: "bigint GENERATED ALWAYS AS IDENTITY"},
		schemaddl.Clause{ID: "new:default", Kind: "column", Name: "label", SQL: "text DEFAULT 'text'"},
		schemaddl.Clause{ID: "new:computed", Kind: "column", Name: "computed", SQL: "bigint GENERATED ALWAYS AS (id+allocated) STORED"})
	before := schemaddl.Signature(original)
	ordinary, identities, all, generated, err := postgresValueProofColumns(original, desired)
	if err != nil || !generated || !reflect.DeepEqual(ordinary, []string{"id", "label"}) || !reflect.DeepEqual(identities, []string{"allocated"}) || !reflect.DeepEqual(all, []string{"id", "allocated", "label", "computed"}) || schemaddl.Signature(original) != before {
		t.Fatal("value proof scopes or original identity changed", ordinary, identities, all, err)
	}
}
