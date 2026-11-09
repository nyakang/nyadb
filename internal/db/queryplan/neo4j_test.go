package queryplan

import (
	"math"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativePlanResultTest() contract.SQLResult {
	zero, exact, ns := int64(0), int64(math.MaxInt64), int64(123456789)
	r := contract.GraphResult{Attempted: true, Completed: true, Truncated: true, Summary: contract.GraphSummary{Plan: &contract.GraphExecutionPlan{Mode: "profile", Query: "CYPHER runtime=slotted PROFILE MATCH (n) RETURN n", Parameters: []contract.GraphProperty{{Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}, Root: contract.GraphPlanOperator{Operator: "ProduceResults", Identifiers: []string{"n", "native_order"}, Arguments: []contract.GraphProperty{{Name: "EstimatedRows", Value: contract.GraphValue{Kind: "float", Text: "1.5"}}, {Name: "Exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}, Profile: &contract.GraphPlanProfile{Rows: &zero, DbHits: &exact, TimeNanoseconds: &ns}, Children: []contract.GraphPlanOperator{{Operator: "First", Profile: &contract.GraphPlanProfile{}}, {Operator: "Second", Profile: &contract.GraphPlanProfile{Rows: &zero}}}}}}}
	return contract.SQLResult{Graph: &r}
}

func TestNeo4jPlanProjectsNativeOrderExactMissingAndRecordedMetrics(t *testing.T) {
	raw := nativePlanResultTest()
	p, err := Parse("neo4j", raw)
	if err != nil || len(p.Nodes) != 1 {
		t.Fatal(err)
	}
	n := p.Nodes[0]
	if p.Engine != "neo4j" || n.Rows == nil || *n.Rows != 1.5 || n.Cost != nil || n.Children[0].Label != "First" || n.Children[1].Label != "Second" || n.Metrics[0].Value.Text != "0" || n.Metrics[1].Value.Text != "9223372036854775807" || n.Metrics[2].Value != nil || n.Metrics[4].Value.Text != "123456789" || n.Metrics[4].Unit != "ns" || n.Metrics[5].Value != nil || !strings.Contains(n.Detail, "9223372036854775807") {
		t.Fatal("native projection inferred estimates, lost precision or fabricated unrecorded metrics")
	}
	raw.Graph.Summary.Plan.Root.Profile.Rows = nil
	raw.Graph.Summary.Plan.Root.Children[0].Operator = "later"
	if n.Metrics[0].Value.Text != "0" || p.Nodes[0].Children[0].Label != "First" {
		t.Fatal("projected plan followed later source")
	}
	c := p.Clone()
	c.Nodes[0].Metrics[0].Value.Text = "later"
	*c.Nodes[0].Rows = 9
	if n.Metrics[0].Value.Text != "0" || *p.Nodes[0].Rows != 1.5 {
		t.Fatal("plan snapshot shared values")
	}
}

func TestNeo4jPlanRejectsIncompleteTreesMalformedMetricsAndArguments(t *testing.T) {
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Graph.Completed = false }, func(r *contract.SQLResult) { r.Graph.Uncertain = true }, func(r *contract.SQLResult) { r.Graph.Summary.PlanCaptureError = "missing tree" },
		func(r *contract.SQLResult) { r.Graph.Summary.Plan.Mode = "guessed" }, func(r *contract.SQLResult) { r.Graph.Summary.Plan.Query = "" },
		func(r *contract.SQLResult) { r.Graph.Summary.Plan.Root.Children[0].Operator = "" }, func(r *contract.SQLResult) { r.Graph.Summary.Plan.Root.Children[0].Profile = nil },
		func(r *contract.SQLResult) { v := int64(-1); r.Graph.Summary.Plan.Root.Profile.Rows = &v }, func(r *contract.SQLResult) { v := math.NaN(); r.Graph.Summary.Plan.Root.Profile.PageCacheHitRatio = &v },
		func(r *contract.SQLResult) { r.Graph.Summary.Plan.Root.Arguments[0].Value.Text = "NaN" }, func(r *contract.SQLResult) { r.Graph.Summary.Plan.Root.Arguments[1].Value.Kind = "invented" },
		func(r *contract.SQLResult) {
			r.Graph.Summary.Plan.Root.Arguments = append(r.Graph.Summary.Plan.Root.Arguments, r.Graph.Summary.Plan.Root.Arguments[0])
		},
		func(r *contract.SQLResult) { r.Graph.Summary.Plan.Parameters[0].Value.Text = "9223372036854775808" },
		func(r *contract.SQLResult) { r.Graph.Summary.Plan.Root.Operator = strings.Repeat("x", 16<<20) },
		func(r *contract.SQLResult) {
			root := &r.Graph.Summary.Plan.Root
			for i := 0; i < 66; i++ {
				root.Children = []contract.GraphPlanOperator{{Operator: "Child", Profile: &contract.GraphPlanProfile{}}}
				root = &root.Children[0]
			}
		},
	} {
		raw := nativePlanResultTest()
		mutate(&raw)
		if p, err := Parse("neo4j", raw); err == nil || p != nil {
			t.Fatal("incomplete native plan projected")
		}
	}
	if p, err := Parse("neo4j", contract.SQLResult{}); err == nil || p != nil {
		t.Fatal("fabricated plan from empty rows")
	}
}
