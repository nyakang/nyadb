package queryplan

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestOpenTenBasePlanPreservesProductAndRemoteChildren(t *testing.T) {
	if _, err := Build("opentenbase", "SELECT 1; DELETE FROM records"); err == nil {
		t.Fatal("multiple statements accepted by native EXPLAIN")
	}
	result := contract.SQLResult{Columns: []string{"QUERY PLAN"}, Rows: [][]string{{`[{"Plan":{"Node Type":"Remote Subquery Scan","Total Cost":2.5,"Plans":[{"Node Type":"Seq Scan","Relation Name":"records","Plan Rows":3}]}}]`}}}
	plan, err := Parse("opentenbase", result)
	if err != nil || plan.Engine != "opentenbase" || len(plan.Nodes) != 1 || len(plan.Nodes[0].Children) != 1 {
		t.Fatal("native plan lost product or remote child", err)
	}
}
