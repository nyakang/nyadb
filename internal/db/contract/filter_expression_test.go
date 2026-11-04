package contract

import "testing"

func TestFilterExpressionPreservesGroupingAndRejectsAmbiguousRequests(t *testing.T) {
	a, b, c := DataFilterRule{Column: "a", Mode: "equal", Value: "1"}, DataFilterRule{Column: "b", Mode: "equal", Value: "2"}, DataFilterRule{Column: "c", Mode: "is_null"}
	expr := &FilterExpression[DataFilterRule]{Op: "and", Children: []FilterExpression[DataFilterRule]{{Op: "or", Children: []FilterExpression[DataFilterRule]{{Op: "rule", Rule: &a}, {Op: "rule", Rule: &b}}}, {Op: "rule", Rule: &c}}}
	rules, err := expr.Rules(6, 3)
	if err != nil || len(rules) != 3 {
		t.Fatal("rules missing", rules, err)
	}
	sql, err := CompileFilter(expr, func(r DataFilterRule) (string, error) { return r.Column + " ?", nil })
	if err != nil || sql != "((a ? OR b ?) AND c ?)" {
		t.Fatal(sql, err)
	}
	if _, err := EffectiveDataExpression(DataOptions{Condition: expr, Filters: []DataFilterRule{a}}); err == nil {
		t.Fatal("ambiguous request accepted")
	}
	if _, err := (&FilterExpression[DataFilterRule]{Op: "or"}).Rules(6, 3); err == nil {
		t.Fatal("empty group accepted")
	}
	cloned := expr.Clone(func(r DataFilterRule) DataFilterRule { return r })
	cloned.Children[0].Children[0].Rule.Value = "changed"
	if a.Value != "1" {
		t.Fatal("expression leaf aliases original")
	}
}

func TestTypedFilterCloneKeepsIndependentBinaryValue(t *testing.T) {
	rule := Filter{Column: "bytes", Operator: "=", Value: Cell{Kind: Binary, Bytes: []byte{1}}}
	expr := AndFilters([]Filter{rule})
	copy := expr.Clone(func(r Filter) Filter { r.Value = r.Value.Clone(); return r })
	copy.Children[0].Rule.Value.Bytes[0] = 2
	if rule.Value.Bytes[0] != 1 {
		t.Fatal("binary value aliases worker")
	}
}
