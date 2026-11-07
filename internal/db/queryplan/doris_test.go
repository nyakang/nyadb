package queryplan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dorisPlanResult(raw string) contract.SQLResult {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "Explain String"}}}
	for _, line := range strings.Split(raw, "\n") {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}

// Native 4.1.3 serialization includes deliberately unbalanced RF notation.
// Optimizer cost belongs to the envelope, not the result sink or join.
const dorisJoinPlan = `cost = 17.40795333333333
PhysicalResultSink[447] ( outputExprs=[note#1, __sum_1#4] )
+--PhysicalHashJoin[415]@3 ( stats=1, type=INNER_JOIN, hashCondition=[(id#0 = id#2)], RFs=[RF0[id#2->[id#0](ndv/size = 1/1) , RF1[id#2->[id#0](ndv/size = 1/1) ] )
   |--PhysicalOlapScan[402]@0 ( table=left_table, stats=1, operativeSlots=[id#0, note#1], virtualColumns=[], JRFs= RF0 RF1 )
   +--PhysicalDistribute[411]@2 ( stats=0.5, distributionSpec=DistributionSpecReplicated )
      +--PhysicalFilter[407]@2 ( stats=0.5, predicates=(n#3 > 0) )
         +--PhysicalOlapScan[403]@1 ( table=right_table, stats=1, operativeSlots=[id#2, n#3], virtualColumns=[] )`

func TestDorisNativeTreeEstimatesAndProperties(t *testing.T) {
	plan, err := Parse("doris", dorisPlanResult(dorisJoinPlan))
	if err != nil || plan.Engine != "doris" || len(plan.Nodes) != 1 {
		t.Fatal("native optimizer tree rejected", err)
	}
	envelope := plan.Nodes[0]
	if envelope.Cost == nil || *envelope.Cost != 17.40795333333333 || envelope.Rows != nil {
		t.Fatal("whole-plan optimizer cost lost or rows invented")
	}
	root := envelope.Children[0]
	join := root.Children[0]
	if root.Cost != nil || root.Rows != nil || join.Cost != nil || *join.Rows != 1 || len(join.Children) != 2 || join.Children[0].Label != "PhysicalOlapScan[402]@0" || *join.Children[1].Rows != 0.5 || !strings.Contains(join.Detail, "RF0[id#2") {
		t.Fatal("operator estimates, native detail or ordered children lost")
	}
	for _, detail := range []string{`stats=null, expressions=[]`, `expressions=['literal, stats=99']`, `predicates=(x = 'stats=99, y')`} {
		r := dorisPlanResult("cost = 0.0\nPhysicalOneRowRelation[25]@0 ( " + detail + " )")
		p, err := Parse("doris", r)
		if err != nil || p.Nodes[0].Children[0].Rows != nil || p.Nodes[0].Children[0].Cost != nil {
			t.Fatal("unknown metrics or literal became estimates", err)
		}
	}
	union := dorisPlanResult(`cost = 4.003
PhysicalUnion[143]@4 ( stats=2, qualifier=ALL )
|--PhysicalDistribute[130]@1 ( stats=1, distributionSpec=DistributionSpecExecutionAny )
|  +--PhysicalProject[126]@1 ( stats=1, projects=[id#0] )
+--PhysicalDistribute[139]@3 ( stats=1, distributionSpec=DistributionSpecExecutionAny )
   +--PhysicalProject[135]@3 ( stats=1, projects=[id#2] )`)
	if p, err := Parse("doris", union); err != nil || len(p.Nodes[0].Children[0].Children) != 2 {
		t.Fatal("continuation bars lost sibling tree", err)
	}
	lazy := dorisPlanResult(`cost = 4.5075199999999995
PhysicalResultSink[156] ( outputExprs=[id#0, note#1] )
+--PhysicalProject[153]@ ( stats=null, projects=[id#0, note#1] )
   +--PhysicalLazyMaterialize [Output= ([id#0, note#1]), lazySlots= ([note#1])]
      +--PhysicalLazyMaterializeOlapScan[PhysicalOlapScan[118]@0 ( table=table1, stats=1, operativeSlots=[id#0], virtualColumns=[] ) RF0 RF1]`)
	p, err := Parse("doris", lazy)
	if err != nil {
		t.Fatal("native lazy materialization rejected", err)
	}
	project := p.Nodes[0].Children[0].Children[0]
	materialize := project.Children[0]
	scan := materialize.Children[0]
	if project.Rows != nil || materialize.Rows != nil || materialize.Label != "PhysicalLazyMaterialize" || scan.Label != "PhysicalLazyMaterializeOlapScan[118]@0" || scan.Rows == nil || *scan.Rows != 1 || !strings.HasSuffix(scan.Detail, "RF0 RF1]") {
		t.Fatal("native lazy labels, unknown or delegated scan statistics lost")
	}
}

func TestDorisPlanRejectsTruncationAmbiguityAndBadTopology(t *testing.T) {
	for _, raw := range []string{
		"", "cost = 0", "cost = NaN\nPhysicalResultSink[1]", "cost = -1\nPhysicalResultSink[1]",
		"cost = +Inf\nPhysicalResultSink[1]", "cost = 1\nnot an operator",
		"cost = 1\nPhysicalResultSink[1]\nPhysicalResultSink[2]",
		"cost = 1\nPhysicalResultSink[1]\n+--PhysicalProject[1]",
		"cost = 1\nPhysicalResultSink[1]\n   +--PhysicalProject[2]",
		"cost = 1\nPhysicalResultSink[1]\n|--PhysicalProject[2]",
		"cost = 1\nPhysicalResultSink[1]\n+--PhysicalProject[2]\n+--PhysicalProject[3]",
		"cost = 1\nPhysicalResultSink[1]\n|--PhysicalProject[2]\n   +--PhysicalProject[3]\n+--PhysicalProject[4]",
		"cost = 1\nPhysicalResultSink[1]\n+--PhysicalProject[2] ( stats=NaN )",
		"cost = 1\nPhysicalResultSink[1]\n+--PhysicalProject[2] ( stats=-1 )",
		"cost = 1\nPhysicalOlapScan[1] ( table=x, stats=99, operativeSlots=x, stats=1, operativeSlots=[] )",
		"cost = 1\nPhysicalResultSink[1]\n\n========== MATERIALIZATIONS ==========",
		"cost = 1\nPhysicalResultSink[1]\n+--PhysicalProject[2]\nembedded newline",
	} {
		if _, err := Parse("doris", dorisPlanResult(raw)); err == nil {
			t.Fatalf("invalid or unsupported tree accepted: %q", raw)
		}
	}
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Truncation = []string{"rows"} },
		func(r *contract.SQLResult) { r.Sets[0].Rows[1][0].Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Rows[1][0].Kind = contract.Null },
		func(r *contract.SQLResult) { r.Sets = append(r.Sets, r.Sets[0]) },
		func(r *contract.SQLResult) { r.Sets[0].Rows[1] = append(r.Sets[0].Rows[1], contract.Cell{}) },
	} {
		r := dorisPlanResult(dorisJoinPlan)
		mutate(&r)
		if _, err := Parse("doris", r); err == nil {
			t.Fatal("incomplete or foreign result accepted")
		}
	}
	deep := "cost = 1\nPhysicalResultSink[0]"
	for i := 1; i <= 65; i++ {
		deep += fmt.Sprintf("\n%s+--PhysicalProject[%d]", strings.Repeat("   ", i-1), i)
	}
	if _, err := Parse("doris", dorisPlanResult(deep)); err == nil {
		t.Fatal("excessively deep plan accepted")
	}
	r := dorisPlanResult("cost = 0\nPhysicalResultSink[1] ( expressions=['" + strings.Repeat("x", 8<<20) + "'] )")
	if _, err := Parse("doris", r); err == nil {
		t.Fatal("oversized raw plan accepted")
	}
}

func TestDorisPlanBuildUsesOneNativeUnexecutedStatement(t *testing.T) {
	for _, source := range []string{"SELECT '中文'", "WITH t AS (SELECT 1) SELECT * FROM t", "INSERT INTO t VALUES(1)", "UPDATE t SET a=1", "DELETE FROM t WHERE a=1", "-- comment\nSELECT 1;"} {
		q, err := Build("doris", source)
		if err != nil || q != "EXPLAIN OPTIMIZED PLAN "+source {
			t.Fatal("native estimated request changed source", q, err)
		}
	}
	for _, source := range []string{"SELECT 1; INSERT INTO t VALUES(2)", "EXPLAIN ANALYZE SELECT 1", "SET x=1", "BEGIN", "CREATE TABLE t(a INT)", "/*! SELECT 1 */", "SELECT 'unterminated", "SELECT '\x00'", "SELECT '\xff'", strings.Repeat(" ", 1<<20) + "SELECT 1"} {
		if _, err := Build("doris", source); err == nil {
			t.Fatal("unsupported or ambiguous request accepted")
		}
	}
}
