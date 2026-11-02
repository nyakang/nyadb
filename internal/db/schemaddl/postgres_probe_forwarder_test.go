package schemaddl

import (
	"strings"
	"testing"
)

const pgProbeForwarderTree = `{QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :rtable <> :cteList <> :hasSubLinks false :isReturn true :jointree {FROMEXPR :fromlist <> :quals <>} :targetList ({TARGETENTRY :expr {OPEXPR :opno 1360 :opfuncid 1272 :opresulttype 1114 :opretset false :opcollid 0 :inputcollid 0 :args ({PARAM :paramkind 0 :paramid 2 :paramtype 1082 :paramtypmod -1 :paramcollid 0 :location -1} {PARAM :paramkind 0 :paramid 1 :paramtype 1083 :paramtypmod -1 :paramcollid 0 :location -1}) :location -1} :resno 1 :resname <> :ressortgroupref 0 :resorigtbl 0 :resorigcol 0 :resjunk false}) :stmt_location -1 :stmt_len -1}`

func TestPostgreSQLProbeOperatorForwarderRequiresBoundReversedParameters(t *testing.T) {
	op, routine, err := PostgreSQLProbeOperatorForwarder(pgProbeForwarderTree, 1083, 1082, 1114)
	if err != nil || op != 1360 || routine != 1272 {
		t.Fatalf("core forwarding identity unavailable: %d %d %v", op, routine, err)
	}
	pg18 := strings.Replace(pgProbeForwarderTree, ":isReturn true", ":isReturn true :hasGroupRTE false :returningOldAlias <> :returningNewAlias <>", 1)
	if op, routine, err := PostgreSQLProbeOperatorForwarder(pg18, 1083, 1082, 1114); err != nil || op != 1360 || routine != 1272 {
		t.Fatal("PostgreSQL 18 inert query fields rejected", err)
	}
	for _, types := range [][3]int64{{1082, 1083, 1114}, {1083, 1082, 1184}, {0, 1082, 1114}} {
		if _, _, err := PostgreSQLProbeOperatorForwarder(pgProbeForwarderTree, types[0], types[1], types[2]); err == nil {
			t.Fatal("unbound operator signature accepted")
		}
	}
}

func TestPostgreSQLProbeOperatorForwarderRejectsAdditionalExecutableSyntax(t *testing.T) {
	for _, change := range [][2]string{
		{":commandType 1", ":commandType 2"},
		{":hasSubLinks false", ":hasSubLinks true"},
		{":utilityStmt <>", ":utilityStmt {CALLSTMT :funcexpr <>}"},
		{":quals <>", ":quals {FUNCEXPR :funcid 99999}"},
		{"OPEXPR", "FUNCEXPR"},
		{":args (", ":args ({CONST :consttype 23} "},
		{":paramid 2", ":paramid 1"},
		{":paramkind 0", ":paramkind 1"},
		{":opretset false", ":opretset true"},
		{":inputcollid 0", ":inputcollid 99999"},
		{":opno 1360", ":opno 0"},
		{":opfuncid 1272", ":opfuncid private_expression"},
		{":isReturn true", ":isReturn true :unknown <>"},
		{":isReturn true", ":isReturn true :hasGroupRTE true"},
		{":isReturn true", ":isReturn true :returningOldAlias private_alias"},
		{":resjunk false", ":resjunk false :resjunk false"},
	} {
		raw := strings.Replace(pgProbeForwarderTree, change[0], change[1], 1)
		if op, routine, err := PostgreSQLProbeOperatorForwarder(raw, 1083, 1082, 1114); err == nil || op != 0 || routine != 0 {
			t.Fatal("unsupported body acquired forwarding permission", change[0])
		}
	}
	for _, raw := range []string{pgProbeForwarderTree + pgProbeForwarderTree, pgProbeForwarderTree[:100], strings.Repeat(" ", 1<<20+1)} {
		if _, _, err := PostgreSQLProbeOperatorForwarder(raw, 1083, 1082, 1114); err == nil {
			t.Fatal("invalid forwarding body accepted")
		}
	}
}
