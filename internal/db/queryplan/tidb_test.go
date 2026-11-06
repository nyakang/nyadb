package queryplan

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestTiDBPlanPreservesHierarchyAndMetrics(t *testing.T) {
	sql, err := Build("tidb", "SELECT id FROM t WHERE id=1")
	if err != nil || !strings.HasPrefix(sql, "EXPLAIN FORMAT=tidb_json ") {
		t.Fatal(sql, err)
	}
	result := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Text: `[{"id":"Projection_1","estRows":"2.5","taskType":"root","subOperators":[{"id":"TableReader_2","estRows":2.5,"estCost":3.75,"accessObject":"table:t","operatorInfo":"keep order:false"}]}]`}}}}}}
	plan, err := Parse("tidb", result)
	if err != nil || plan.Engine != "tidb" || len(plan.Nodes) != 1 || len(plan.Nodes[0].Children) != 1 {
		t.Fatal(plan, err)
	}
	root, child := plan.Nodes[0], plan.Nodes[0].Children[0]
	if root.Rows == nil || *root.Rows != 2.5 || root.Cost != nil || child.Cost == nil || *child.Cost != 3.75 || !strings.Contains(child.Detail, "table:t") {
		t.Fatal("lost or invented plan metrics")
	}
	for _, raw := range []string{`[]`, `[{}]`, `[{"id":"x"},{"id":"x"}]`, `[{"id":"x","subOperators":[{"id":"x"}]}]`, `[{"id":`} {
		result.Sets[0].Rows[0][0].Text = raw
		if _, err = Parse("tidb", result); err == nil {
			t.Fatal("invalid plan accepted", raw)
		}
	}
	result.Sets[0].Rows[0][0] = contract.Cell{Text: `[{"id":"x"}]`, Truncated: true}
	if _, err = Parse("tidb", result); err == nil {
		t.Fatal("truncated plan accepted")
	}
}
