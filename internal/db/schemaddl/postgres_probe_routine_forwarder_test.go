package schemaddl

import (
	"reflect"
	"strings"
	"testing"
)

const pgProbeRoundExpression = `{FUNCEXPR :funcid 1707 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({PARAM :paramkind 0 :paramid 1 :paramtype 1700 :paramtypmod -1 :paramcollid 0 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`
const pgProbeSQLReturnPrefix = `{QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :rtable <> :cteList <> :isReturn true :jointree {FROMEXPR :fromlist <> :quals <>} :targetList ({TARGETENTRY :expr `
const pgProbeSQLReturnSuffix = ` :resno 1 :resname <> :ressortgroupref 0 :resorigtbl 0 :resorigcol 0 :resjunk false}) :stmt_location -1 :stmt_len -1}`

func TestPostgreSQLProbeRoutineForwarderBindsNestedSignatures(t *testing.T) {
	raw := pgProbeSQLReturnPrefix + pgProbeRoundExpression + pgProbeSQLReturnSuffix
	want := []PostgreSQLProbeRoutineSignature{{OID: 1707, Result: 1700, Arguments: []int64{1700, 23}}}
	calls, err := PostgreSQLProbeRoutineForwarder(raw, []int64{1700}, 1700)
	if err != nil || !reflect.DeepEqual(calls, want) {
		t.Fatalf("core round body rejected: %v %v", calls, err)
	}
	// An outer call cannot conceal the inner implementation or argument type.
	expr := `{FUNCEXPR :funcid 1709 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args (` + pgProbeRoundExpression + ` {PARAM :paramkind 0 :paramid 2 :paramtype 23 :paramtypmod -1 :paramcollid 0 :location -1}) :location -1}`
	calls, err = PostgreSQLProbeRoutineForwarder(pgProbeSQLReturnPrefix+expr+pgProbeSQLReturnSuffix, []int64{1700, 23}, 1700)
	want = append(want, PostgreSQLProbeRoutineSignature{OID: 1709, Result: 1700, Arguments: []int64{1700, 23}})
	if err != nil || !reflect.DeepEqual(calls, want) {
		t.Fatalf("nested forwarding signature lost: %v %v", calls, err)
	}
	for _, types := range [][]int64{nil, {23}, {1700, 20000}} {
		if _, err := PostgreSQLProbeRoutineForwarder(raw, types, 1700); err == nil {
			t.Fatal("unbound parameters accepted")
		}
	}
	if _, err := PostgreSQLProbeRoutineForwarder(raw, []int64{1700}, 23); err == nil {
		t.Fatal("wrong result accepted")
	}
}

func TestPostgreSQLProbeRoutineForwarderRejectsExecutableExtras(t *testing.T) {
	raw := pgProbeSQLReturnPrefix + pgProbeRoundExpression + pgProbeSQLReturnSuffix
	for _, change := range [][2]string{
		{":funcid 1707", ":funcid 20000"}, {":funcid 1707", ":funcid 0"},
		{":funcretset false", ":funcretset true"}, {":funcvariadic false", ":funcvariadic true"},
		{":funcformat 0", ":funcformat 2"}, {":inputcollid 0", ":inputcollid 20000"},
		{":paramid 1", ":paramid 2"}, {":paramtype 1700", ":paramtype 23"},
		{":paramkind 0", ":paramkind 1"}, {":consttype 23", ":consttype 2205"},
		{":constbyval true", ":constbyval false"}, {":constisnull false", ":constisnull true"},
		{"[ 0 0 0 0 0 0 0 0 ]", "[ 1 0 0 0 0 0 0 0 ]"},
		{":quals <>", ":quals {FUNCEXPR :funcid 20000}"},
		{":isReturn true", ":isReturn true :hasModifyingCTE true"},
		{":funcid 1707", ":funcid 1707 :unknown <>"}, {"FUNCEXPR", "COERCEVIAIO"},
	} {
		if _, err := PostgreSQLProbeRoutineForwarder(strings.Replace(raw, change[0], change[1], 1), []int64{1700}, 1700); err == nil {
			t.Fatal("executable extra accepted", change[0])
		}
	}
}
