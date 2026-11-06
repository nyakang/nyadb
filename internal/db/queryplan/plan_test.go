package queryplan

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestPlansUseRealMetricsAndBoundReadOnlyRequests(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql", "sqlite"} {
		q, err := Build(engine, "SELECT '中文' FROM t")
		if err != nil || strings.Contains(q, "ANALYZE") {
			t.Fatalf("unsafe %s request: %s %v", engine, q, err)
		}
	}
	samples := map[string]string{"mysql": `{"query_block":{"cost_info":{"query_cost":"4.5"},"nested_loop":[{"table":{"table_name":"t","access_type":"ALL","rows_examined_per_scan":42}}]}}`, "postgresql": `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"t","Plan Rows":42,"Total Cost":4.5}}]`}
	for engine, raw := range samples {
		result := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "plan"}}, Rows: [][]contract.Cell{{{Kind: contract.JSON, Text: raw}}}}}}
		plan, err := Parse(engine, result)
		if err != nil || len(plan.Nodes) == 0 {
			t.Fatalf("%s parse: %v", engine, err)
		}
		result.Sets[0].Rows[0][0].Truncated = true
		if _, err = Parse(engine, result); err == nil {
			t.Fatal("truncated plan accepted")
		}
	}
	result := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "id"}, {Name: "parent"}, {Name: "unused"}, {Name: "detail"}}, Rows: [][]contract.Cell{{{Text: "2"}, {Text: "0"}, {Text: "0"}, {Text: "SCAN t"}}}}}}
	plan, err := Parse("sqlite", result)
	if err != nil || plan.Nodes[0].Rows != nil || plan.Nodes[0].Cost != nil || plan.Nodes[0].Label != "SCAN t" {
		t.Fatal("SQLite metrics invented", plan, err)
	}
}

func TestMariaDBPlanTraversesSortAndMaterializationWithRealRows(t *testing.T) {
	q, e := Build("mariadb", "SELECT `t`.`id` FROM `t` ORDER BY id")
	if e != nil || !strings.HasPrefix(q, "EXPLAIN FORMAT=JSON ") {
		t.Fatal(q, e)
	}
	raw := `{"query_block":{"read_sorted_file":{"filesort":{"sort_key":"t.id","table":{"table_name":"t","access_type":"ALL","rows":42}}},"subqueries":[{"expression_cache":{"query_block":{"table":{"table_name":"s","access_type":"ref","rows":3}}}}]}}`
	result := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.JSON, Text: raw}}}}}}
	plan, e := Parse("mariadb", result)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]float64{}
	var walk func([]contract.PlanNode)
	walk = func(nodes []contract.PlanNode) {
		for _, n := range nodes {
			if n.Rows != nil {
				seen[n.Label] = *n.Rows
			}
			if n.Cost != nil {
				t.Fatal("invented MariaDB cost")
			}
			walk(n.Children)
		}
	}
	walk(plan.Nodes)
	if seen["t · ALL"] != 42 || seen["s · ref"] != 3 {
		t.Fatal("MariaDB plan lost nested metrics", seen)
	}
}
