package queryplan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func starRocksResult(raw string) contract.SQLResult {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "Explain String"}}}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}

func starRocksFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "starrocks", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestStarRocksNativeFragmentsCardinalitiesAndCosts(t *testing.T) {
	for _, name := range []string{"constant", "scan", "topn", "join_aggregate", "union", "cte_join", "window", "insert", "update", "delete", "multicast_cte", "nested_join", "nested_side_branches"} {
		t.Run(name, func(t *testing.T) {
			result := starRocksResult(starRocksFixture(t, name))
			plan, err := Parse("starrocks", result)
			if err != nil || plan.Engine != "starrocks" || len(plan.Nodes) != 1 {
				t.Fatalf("native COSTS layout rejected: %v", err)
			}
			root := plan.Nodes[0]
			if root.Cost != nil || root.Rows != nil || len(root.Children) < 2 {
				t.Fatal("whole-plan metrics copied to envelope")
			}
			cost := root.Children[0]
			if cost.Label != "PLAN COST" || len(cost.Children) != 2 || cost.Children[0].Label != "CPU" || cost.Children[1].Label != "Memory" || cost.Children[0].Cost == nil || cost.Children[1].Cost == nil {
				t.Fatal("CPU/memory optimizer costs lost or combined")
			}
			var walk func(contract.PlanNode)
			walk = func(n contract.PlanNode) {
				if n.Rows == nil || n.Cost != nil {
					t.Fatal("native cardinality missing or operator cost fabricated")
				}
				for _, child := range n.Children {
					walk(child)
				}
			}
			for i, fragment := range root.Children[1:] {
				if !strings.HasPrefix(fragment.Label, fmt.Sprintf("PLAN FRAGMENT %d(", i)) || !strings.Contains(fragment.Detail, "Input Partition:") || fragment.Rows != nil || fragment.Cost != nil || len(fragment.Children) != 1 {
					t.Fatal("native fragment identity or properties lost")
				}
				walk(fragment.Children[0])
			}
		})
	}
	plan, err := Parse("starrocks", starRocksResult(starRocksFixture(t, "join_aggregate")))
	if err != nil {
		t.Fatal(err)
	}
	root := plan.Nodes[0]
	if *root.Children[0].Children[0].Cost != 257.6 || *root.Children[0].Children[1].Cost != 90.2 {
		t.Fatal("optimizer costs changed")
	}
	join := root.Children[3].Children[0].Children[0].Children[0]
	if join.Label != "3:HASH JOIN" || len(join.Children) != 2 || join.Children[0].Label != "0:OlapScanNode" || join.Children[1].Label != "2:EXCHANGE" || !strings.Contains(join.Children[0].Detail, "table: plan_b") || !strings.Contains(join.Detail, "BUCKET_SHUFFLE") {
		t.Fatal("main/side branch order or native join properties lost")
	}
	nested, err := Parse("starrocks", starRocksResult(starRocksFixture(t, "nested_side_branches")))
	if err != nil {
		t.Fatal(err)
	}
	outer := nested.Nodes[0].Children[2].Children[0]
	inner := outer.Children[1].Children[0]
	if outer.Label != "5:HASH JOIN" || outer.Children[0].Label != "0:OlapScanNode" || inner.Label != "3:HASH JOIN" || inner.Children[0].Label != "1:OlapScanNode" || inner.Children[1].Label != "2:OlapScanNode" {
		t.Fatal("nested side/main branches borrowed the last emitted parent")
	}
	unknown := strings.Replace(starRocksFixture(t, "constant"), "cardinality: 1", "cardinality: -1", 1)
	plan, err = Parse("starrocks", starRocksResult(unknown))
	if err != nil || plan.Nodes[0].Children[1].Children[0].Rows != nil {
		t.Fatal("unknown cardinality became a measured value")
	}
}

func TestStarRocksIncompleteAmbiguousAndForeignPlansStayRaw(t *testing.T) {
	raw := starRocksFixture(t, "cte_join")
	for _, invalid := range []string{
		strings.Replace(raw, "PLAN COST", "PLAN PROFILE", 1),
		strings.Replace(raw, "CPU: 96.0", "CPU: NaN", 1),
		strings.Replace(raw, "Memory: 16.0", "Memory: +Inf", 1),
		strings.Replace(raw, "Memory: 16.0", "Memory: -1", 1),
		strings.Replace(raw, "PLAN FRAGMENT 1(F00)", "PLAN FRAGMENT 2(F00)", 1),
		strings.Replace(raw, "PLAN FRAGMENT 1(F00)", "PLAN FRAGMENT 1(F02)", 1),
		strings.Replace(raw, "OutPut Exchange Id: 04", "OutPut Exchange Id: 999", 1),
		strings.Replace(raw, "|----1:OlapScanNode", "|----0:OlapScanNode", 1),
		strings.Replace(raw, "|----1:OlapScanNode", "|    |----1:OlapScanNode", 1),
		strings.Replace(raw, "     cardinality: 2", "     cardinality: -2", 1),
		strings.Replace(raw, "     cardinality: 2", "     cardinality: 1.5", 1),
		strings.Replace(raw, "     cardinality: 2", "     cardinality: 9223372036854775808", 1),
		strings.Replace(raw, "     cardinality: 2", "     cardinality: 2\n     cardinality: 99", 1),
		strings.Replace(raw, "     cardinality: 2", "     output column: 'cardinality: 99'", 1),
		raw[:strings.LastIndex(raw, "  0:OlapScanNode")],
		raw[:strings.Index(raw, "PLAN FRAGMENT 1(F00)")],
		"cost = 1\nPhysicalOneRowRelation[0]",
	} {
		if _, err := Parse("starrocks", starRocksResult(invalid)); err == nil {
			t.Fatal("ambiguous or incomplete native layout accepted")
		}
	}
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Truncation = []string{"rows"} },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Kind = contract.Null },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Bytes = []byte("opaque") },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0] = append(r.Sets[0].Rows[0], contract.Cell{}) },
		func(r *contract.SQLResult) { r.Sets = append(r.Sets, r.Sets[0]) },
	} {
		result := starRocksResult(raw)
		mutate(&result)
		if _, err := Parse("starrocks", result); err == nil {
			t.Fatal("truncated or foreign result envelope accepted")
		}
	}
	deep := "PLAN COST\n  CPU: 1\n  Memory: 0\nPLAN FRAGMENT 0(F00)\n  Input Partition: RANDOM\n"
	for i := 0; i < 65; i++ {
		deep += fmt.Sprintf("  %d:Project\n  |  cardinality: 1\n", i)
	}
	deep += "  65:OlapScanNode\n     cardinality: 1\n"
	if _, err := Parse("starrocks", starRocksResult(deep)); err == nil {
		t.Fatal("deep unary tree passed the branch-width depth guard")
	}
	cycle := "PLAN COST\n  CPU: 1\n  Memory: 0\nPLAN FRAGMENT 0(F00)\n  Input Partition: RANDOM\n  OutPut Exchange Id: 1\n  0:EXCHANGE\n     cardinality: 1\nPLAN FRAGMENT 1(F01)\n  Input Partition: RANDOM\n  OutPut Exchange Id: 0\n  1:EXCHANGE\n     cardinality: 1"
	if _, err := Parse("starrocks", starRocksResult(cycle)); err == nil {
		t.Fatal("cyclic fragment producers accepted")
	}
	oversized := strings.Replace(raw, "PLAN COST", "PLAN COST"+strings.Repeat("x", 8<<20), 1)
	if _, err := Parse("starrocks", starRocksResult(oversized)); err == nil {
		t.Fatal("oversized raw plan accepted")
	}
}

func TestStarRocksBuildRequestsOneNativeEstimatedStatement(t *testing.T) {
	for _, source := range []string{"SELECT '中文'", "WITH t AS(SELECT 1) SELECT * FROM t", "INSERT INTO t VALUES(1)", "UPDATE t SET a=1", "DELETE FROM t WHERE a=1", "-- comment\nSELECT 1;"} {
		q, err := Build("starrocks", source)
		if err != nil || q != "EXPLAIN COSTS "+source || strings.Contains(q, "ANALYZE") {
			t.Fatal("native estimated request changed source", q, err)
		}
	}
	for _, source := range []string{"SELECT 1; DELETE FROM t", "EXPLAIN ANALYZE SELECT 1", "ANALYZE TABLE t", "SET x=1", "BEGIN", "CREATE TABLE t(a INT)", "/*! SELECT 1 */", "SELECT 'unterminated", "SELECT '\x00'", "SELECT '\xff'", strings.Repeat(" ", 1<<20) + "SELECT 1"} {
		if _, err := Build("starrocks", source); err == nil {
			t.Fatal("unsupported or ambiguous plan request accepted")
		}
	}
}
