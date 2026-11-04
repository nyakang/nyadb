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
