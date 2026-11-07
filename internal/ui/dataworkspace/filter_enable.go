package dataworkspace

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"slices"
)

func dataRuleProblem(t *Document, r contract.DataFilterRule) string {
	if t != nil && t.boundRuntime != nil && t.boundRuntime.FilterColumns != nil && !slices.Contains(t.boundRuntime.FilterColumns, r.Column) {
		return locales.Text("此字段不支持筛选")
	}
	valid := false
	for _, c := range t.columns {
		valid = valid || c.ID == r.Column
	}
	if !valid {
		return locales.Text("筛选字段不存在或未选择")
	}
	if !t.supportsFilter(r.Mode) || len(r.Value) > 4096 {
		return locales.Text("筛选条件无效或过长")
	}
	return ""
}

// Disabled UI rows are pruned before the neutral expression reaches adapters.
func effectiveFilterGroup(t *Document, e *contract.FilterExpression[contract.DataFilterRule]) *contract.FilterExpression[contract.DataFilterRule] {
	if e == nil {
		return nil
	}
	if e.Rule != nil {
		if t.disabledGroupRules[e.Rule] {
			return nil
		}
		return cloneDataCondition(e)
	}
	out := &contract.FilterExpression[contract.DataFilterRule]{Op: e.Op}
	for i := range e.Children {
		if x := effectiveFilterGroup(t, &e.Children[i]); x != nil {
			out.Children = append(out.Children, *x)
		}
	}
	if len(out.Children) == 0 {
		return nil
	}
	return out
}
