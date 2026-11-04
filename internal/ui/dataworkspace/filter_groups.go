package dataworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

func cloneDataCondition(e *contract.FilterExpression[contract.DataFilterRule]) *contract.FilterExpression[contract.DataFilterRule] {
	return e.Clone(func(rule contract.DataFilterRule) contract.DataFilterRule { return rule })
}
func dataConditionCount(e *contract.FilterExpression[contract.DataFilterRule]) int {
	if e == nil {
		return 0
	}
	if e.Rule != nil {
		return 1
	}
	n := 0
	for i := range e.Children {
		n += dataConditionCount(&e.Children[i])
	}
	return n
}
func dataConditionSummary(e *contract.FilterExpression[contract.DataFilterRule]) string {
	if e == nil {
		return ""
	}
	if e.Rule != nil {
		return e.Rule.Column + " " + mysqlDataFilterModeLabel(e.Rule.Mode)
	}
	parts := []string{}
	for i := range e.Children {
		parts = append(parts, dataConditionSummary(&e.Children[i]))
	}
	return "(" + strings.Join(parts, " "+strings.ToUpper(e.Op)+" ") + ")"
}

func (s *State) beginFilterGroups(t *Document) {
	if t.filterCondition != nil {
		t.groupDraft = cloneDataCondition(t.filterCondition)
		return
	}
	rules := []contract.DataFilterRule{}
	if t.filterColumnDraft != "" && t.filterColumnDraft != "选择字段" {
		rules = append(rules, contract.DataFilterRule{Column: t.filterColumnDraft, Mode: mysqlFilterMode(t.filterModeDraft), Value: t.filterValueDraft})
	}
	for _, d := range t.filterExtraDraft {
		rules = append(rules, contract.DataFilterRule{Column: d.Column, Mode: mysqlFilterMode(d.Mode), Value: d.Value})
	}
	t.groupDraft = contract.AndFilters(rules)
	if t.groupDraft == nil {
		t.groupDraft = &contract.FilterExpression[contract.DataFilterRule]{Op: "and"}
	}
}

func (s *State) filterGroupView(c *ui.Context, t *Document, node *contract.FilterExpression[contract.DataFilterRule], depth int, path string, columns []string) {
	ui.Column(c).Key(path).Gap(4).Padding(2, 8).Border(1, theme.Colors(c).Border).Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			selected := strings.ToUpper(node.Op)
			if ui.Select(c, &selected, []string{"AND", "OR"}).Label(locales.Text("条件组关系") + path).Width(90).Changed() {
				node.Op = strings.ToLower(selected)
			}
			if ui.Button(c, locales.Text("添加条件")).Label(locales.Text("组内添加条件") + path).Disabled(dataConditionCount(t.groupDraft) >= contract.DataMaxFilterRules).Clicked() {
				rule := contract.DataFilterRule{Mode: mysqlFilterMode(t.defaultFilter())}
				node.Children = append(node.Children, contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule})
			}
			if ui.Button(c, locales.Text("添加条件组")).Label(locales.Text("添加条件组") + path).Disabled(depth >= 3 || dataConditionCount(t.groupDraft) >= contract.DataMaxFilterRules).Clicked() {
				node.Children = append(node.Children, contract.FilterExpression[contract.DataFilterRule]{Op: "and"})
			}
		})
		remove := -1
		for i := range node.Children {
			child := &node.Children[i]
			key := fmt.Sprintf("%s/%d", path, i)
			ui.Row(c).Key(key).Wrap().Gap(4).Children(func() {
				if child.Rule != nil {
					f := child.Rule
					mysqlDataRuleSelect(c, &f.Column, append([]string{"选择字段"}, columns...), mysqlDataColumnChoiceLabel).Label(locales.Text("筛选字段") + key).Width(150)
					mode := mysqlDataFilterModeLabel(f.Mode)
					if mysqlDataRuleSelect(c, &mode, t.filterChoices(), locales.Text).Width(110).Changed() {
						f.Mode = mysqlFilterMode(mode)
					}
					if f.Mode != "is_null" && f.Mode != "is_not_null" {
						input := ui.TextInput(c, &f.Value).Label(locales.Text("筛选值") + key).Width(170)
						root := t.groupDraft
						theme.BindNativeEdit(c, input, fmt.Sprintf("data/filter-group/%d/%p", t.id, f), s.deps.EditFocus, s.deps.EditTarget, func() bool {
							if t.closed || t.groupDraft == nil || t.groupDraft != root {
								return false
							}
							var contains func(*contract.FilterExpression[contract.DataFilterRule]) bool
							contains = func(e *contract.FilterExpression[contract.DataFilterRule]) bool {
								if e.Rule == f {
									return true
								}
								for i := range e.Children {
									if contains(&e.Children[i]) {
										return true
									}
								}
								return false
							}
							return contains(root)
						}, true, false)
					}
				} else {
					s.filterGroupView(c, t, child, depth+1, key, columns)
				}
				if ui.Button(c, "×").Label(locales.Text("删除条件或组") + key).Clicked() {
					remove = i
				}
			})
		}
		if remove >= 0 {
			node.Children = append(node.Children[:remove], node.Children[remove+1:]...)
		}
	})
}

func (s *State) validateFilterGroups(t *Document) ([]contract.DataFilterRule, bool) {
	rules, err := t.groupDraft.Rules(contract.DataMaxFilterRules, 3)
	if err != nil {
		t.ruleError = locales.Text("条件组不能为空，最多 6 个条件和 3 层分组")
		return nil, false
	}
	for _, rule := range rules {
		valid := false
		for _, column := range t.columns {
			valid = valid || column.ID == rule.Column
		}
		if !valid || !t.supportsFilter(rule.Mode) || len(rule.Value) > 4096 {
			t.ruleError = locales.Text("筛选条件无效或过长")
			return nil, false
		}
	}
	return rules, true
}

func removeConditionField(e *contract.FilterExpression[contract.DataFilterRule], name string) *contract.FilterExpression[contract.DataFilterRule] {
	if e == nil {
		return nil
	}
	if e.Rule != nil {
		if e.Rule.Column == name {
			return nil
		}
		return cloneDataCondition(e)
	}
	out := &contract.FilterExpression[contract.DataFilterRule]{Op: e.Op}
	for i := range e.Children {
		if child := removeConditionField(&e.Children[i], name); child != nil {
			out.Children = append(out.Children, *child)
		}
	}
	if len(out.Children) == 0 {
		return nil
	}
	return out
}

func (s *State) applyGroupedCellFilter(services ui.Services, t *Document, name, mode, value string) bool {
	if t.filterCondition == nil {
		return false
	}
	condition := removeConditionField(t.filterCondition, name)
	rule := contract.DataFilterRule{Column: name, Mode: mode, Value: value}
	leaf := contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}
	if condition == nil {
		condition = &contract.FilterExpression[contract.DataFilterRule]{Op: "and", Children: []contract.FilterExpression[contract.DataFilterRule]{leaf}}
	} else if condition.Op == "and" {
		condition.Children = append(condition.Children, leaf)
	} else {
		condition = &contract.FilterExpression[contract.DataFilterRule]{Op: "and", Children: []contract.FilterExpression[contract.DataFilterRule]{*condition, leaf}}
	}
	if _, err := condition.Rules(contract.DataMaxFilterRules, 3); err != nil {
		t.ruleError = locales.Text("条件组不能为空，最多 6 个条件和 3 层分组")
		return true
	}
	t.filterCondition = condition
	t.groupDraft = cloneDataCondition(condition)
	t.ruleError = ""
	s.Refresh(services, t)
	return true
}
