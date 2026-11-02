package schemaddl

import (
	"reflect"
	"strings"
	"testing"
)

func TestPostgreSQLValueRoutineEvidenceCapturesNestedDispatch(t *testing.T) {
	raw := `{OPEXPR :opfuncid 177 :args ({FUNCEXPR :funcid 1707 :args ({CONST :consttype 23 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]})} {FUNCEXPR :funcid 1707})}`
	calls, err := PostgreSQLValueExpressionRoutines(raw)
	if err != nil || !reflect.DeepEqual(calls, []int64{177, 1707}) {
		t.Fatal("value dispatch evidence incomplete", calls, err)
	}
	for _, raw := range []string{`{SQLVALUEFUNCTION :op 0 :type 1082 :typmod -1}`, `{CONST :consttype 25 :constvalue 4 [ 0 0 0 0 ]}`} {
		calls, err := PostgreSQLValueExpressionRoutines(raw)
		if err != nil || len(calls) != 0 {
			t.Fatal("transaction constant rejected", calls, err)
		}
	}
}
func TestPostgreSQLValueRoutineEvidenceRejectsStatefulAndUnknownTrees(t *testing.T) {
	for _, raw := range []string{
		`{NEXTVALUEEXPR :seqid 17}`, `{SQLVALUEFUNCTION :op 15}`, `{SQLVALUEFUNCTION :type 25}`,
		`{FUNCEXPR :funcid 20000}`, `{FUNCEXPR :funcid 0}`, `{FUNCEXPR :args <>}`,
		`{UNKNOWNEXPR :args ({FUNCEXPR :funcid 1707})}`, `{AGGREF :aggfnoid 1}`, `{QUERY :commandType 1}`,
		`{OPEXPR :opfuncid 177}{OPEXPR :opfuncid 177}`, strings.Repeat(" ", 1<<20+1),
	} {
		if _, err := PostgreSQLValueExpressionRoutines(raw); err == nil {
			t.Fatal("unproven expression acquired repeatability", raw[:min(50, len(raw))])
		}
	}
}

func TestPostgreSQLValueRoutineEvidenceTraversesLegacyArrays(t *testing.T) {
	for _, name := range []string{"ARRAY", "ARRAYEXPR"} {
		raw := `{` + name + ` :array_typeid 1231 :array_collid 0 :element_typeid 1700 :elements ({CONST :consttype 1700 :constisnull true :constvalue <>} {FUNCEXPR :funcid 1707 :args ({CONST :consttype 1700 :constvalue 4 [ 0 0 0 0 ]})}) :multidims false :location 1}`
		calls, err := PostgreSQLValueExpressionRoutines(raw)
		if err != nil || !reflect.DeepEqual(calls, []int64{1707}) {
			t.Fatal("legacy array lost nested routine evidence", name, calls, err)
		}
		raw = strings.Replace(raw, ":funcid 1707", ":funcid 20000", 1)
		if _, err := PostgreSQLValueExpressionRoutines(raw); err == nil {
			t.Fatal("array hid unproven nested dispatch", name)
		}
	}
}
