package contract

import (
	"reflect"
	"testing"
)

func TestGraphPlanCloneKeepsIndependentArgumentsParametersChildrenAndMetrics(t *testing.T) {
	zero := int64(0)
	ratio := 0.0
	r := GraphResult{Summary: GraphSummary{Counters: map[string]int64{}, Plan: &GraphExecutionPlan{Mode: "profile", Query: "PROFILE RETURN $value", Parameters: []GraphProperty{{Name: "value", Value: GraphValue{Kind: "bytes", Text: "AP8="}}}, Root: GraphPlanOperator{Operator: "Root", Arguments: []GraphProperty{{Name: "nested", Value: GraphValue{Kind: "map", Properties: []GraphProperty{{Name: "exact", Value: GraphValue{Kind: "integer", Text: "9223372036854775807"}}}}}}, Identifiers: []string{"original"}, Children: []GraphPlanOperator{{Operator: "Child"}}, Profile: &GraphPlanProfile{Rows: &zero, PageCacheHitRatio: &ratio}}}}}
	before := r.Clone()
	copy := r.Clone()
	copy.Summary.Plan.Parameters[0].Value.Text = "later"
	copy.Summary.Plan.Root.Arguments[0].Value.Properties[0].Value.Text = "1"
	copy.Summary.Plan.Root.Identifiers[0] = "later"
	copy.Summary.Plan.Root.Children[0].Operator = "later"
	*copy.Summary.Plan.Root.Profile.Rows = 2
	*copy.Summary.Plan.Root.Profile.PageCacheHitRatio = 0.5
	if !reflect.DeepEqual(r, before) || r.Summary.Plan.Root.Profile.DbHits != nil {
		t.Fatal("clone lost native missing metrics or retained shared payload")
	}
}
