package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type mysqlDataRuleDraft struct {
	Column, Mode, Value string
}
type mysqlDataSortDraft struct {
	Column, Direction string
}

var mysqlFilterModeChoices = []string{"包含", "精确匹配", "不等于", "为空", "不为空"}

func mysqlFilterMode(mode string) string {
	switch mode {
	case "包含":
		return "contains"
	case "精确匹配":
		return "equal"
	case "不等于":
		return "not_equal"
	case "为空":
		return "is_null"
	case "不为空":
		return "is_not_null"
	}
	return ""
}
func mysqlDataFilterModeNeedsValue(mode string) bool { return mode != "为空" && mode != "不为空" }

func (s *State) mysqlDataRuleEditor(c *ui.Context, t *tab, page *connections.MySQLDataPage) {
	ui.Column(c).Background(theme.Header).Padding(5, 9).Gap(5).Children(func() {
		ui.Row(c).Height(27).Gap(8).AlignItems(ui.Center).Children(func() {
			label := "筛选 & 排序 ▸"
			if t.dataRulesExpanded {
				label = "筛选 & 排序 ▾"
			}
			if ui.Button(c, label).Label("筛选 & 排序").Clicked() {
				t.dataRulesExpanded = !t.dataRulesExpanded
			}
			count := len(t.dataFilterRules)
			if count == 0 && t.dataFilterMode != "" {
				count = 1
			}
			ui.Text(c, fmt.Sprintf("筛选 %d · 排序 %d", count, mysqlDataActiveSortCount(t))).FontSize(11).TextColor(theme.Muted)
			if !t.dataRulesExpanded && t.dataRuleError != "" {
				ui.Text(c, t.dataRuleError).SingleLine().FontSize(11).TextColor(theme.Gold)
			}
			ui.Box(c).Grow(1)
			if ui.Button(c, "清除筛选").Disabled(t.dataLoading || count == 0).Clicked() {
				s.clearMySQLDataFilter(c.Services(), t)
			}
		})
		if !t.dataRulesExpanded {
			return
		}
		choices := append([]string{"选择字段"}, page.Columns...)
		ui.Text(c, "筛选  ·  所有已启用条件均满足 (AND)").FontSize(11).TextColor(theme.Muted)
		ui.Row(c).Height(32).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Select(c, &t.dataFilterColumnDraft, choices).Width(160)
			ui.Select(c, &t.dataFilterModeDraft, mysqlFilterModeChoices).Width(110)
			if mysqlDataFilterModeNeedsValue(t.dataFilterModeDraft) {
				ui.TextInput(c, &t.dataFilterValueDraft).Label("数据筛选值").Placeholder("筛选值").Width(190)
			}
			ui.Box(c).Grow(1)
			if ui.Button(c, "添加条件").Label("添加筛选规则").Disabled(len(t.dataFilterExtraDraft)+1 >= connections.MySQLDataMaxFilterRules).Clicked() {
				t.dataFilterExtraDraft = append(t.dataFilterExtraDraft, mysqlDataRuleDraft{Column: "选择字段", Mode: "包含"})
			}
		})
		for i := range t.dataFilterExtraDraft {
			i := i
			rule := &t.dataFilterExtraDraft[i]
			ui.Row(c).Height(32).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "且").TextColor(theme.Muted).FontSize(11)
				ui.Select(c, &rule.Column, choices).Width(140)
				ui.Select(c, &rule.Mode, mysqlFilterModeChoices).Width(110)
				if mysqlDataFilterModeNeedsValue(rule.Mode) {
					ui.TextInput(c, &rule.Value).Label(fmt.Sprintf("附加筛选值 %d", i+1)).Placeholder("筛选值").Width(180)
				}
				if ui.Button(c, "移除").Label(fmt.Sprintf("删除筛选规则 %d", i+2)).Clicked() {
					t.dataFilterExtraDraft = append(t.dataFilterExtraDraft[:i], t.dataFilterExtraDraft[i+1:]...)
				}
			})
		}
		ui.Divider(c)
		ui.Row(c).Height(26).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "排序方式 · 按规则优先级依次排序").FontSize(11).TextColor(theme.Muted)
			ui.Box(c).Grow(1)
			if ui.Button(c, "添加排序").Disabled(len(t.dataSortDraft) >= connections.MySQLDataMaxSortRules).Clicked() {
				t.dataSortDraft = append(t.dataSortDraft, mysqlDataSortDraft{Column: "选择字段", Direction: "升序"})
			}
		})
		for i := range t.dataSortDraft {
			i := i
			rule := &t.dataSortDraft[i]
			ui.Row(c).Height(32).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("%d.", i+1)).FontSize(11).TextColor(theme.Muted)
				ui.Select(c, &rule.Column, choices).Width(165)
				ui.Select(c, &rule.Direction, []string{"升序", "降序"}).Width(100)
				if ui.Button(c, "移除").Label(fmt.Sprintf("删除排序规则 %d", i+1)).Clicked() {
					t.dataSortDraft = append(t.dataSortDraft[:i], t.dataSortDraft[i+1:]...)
				}
			})
		}
		ui.Row(c).Height(31).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "应用筛选 & 排序").Label("应用筛选").Disabled(t.dataLoading).Clicked() {
				s.applyMySQLDataFilter(c.Services(), t)
			}
			if t.dataRuleError != "" {
				ui.Text(c, t.dataRuleError).FontSize(11).TextColor(theme.Gold)
			}
			if summary := mysqlDataFilterStatus(t); summary != "" {
				ui.Text(c, "当前: "+summary).SingleLine().FontSize(11).TextColor(theme.Muted)
			}
		})
	})
}

func mysqlDataActiveSortCount(t *tab) int {
	if len(t.dataSortRules) > 0 {
		return len(t.dataSortRules)
	}
	if t.dataSortColumn != "" {
		return 1
	}
	return 0
}

func (s *State) applyMySQLDataRules(services ui.Services, t *tab) {
	if t == nil || (t.dataPage == nil && len(t.dataColumns) == 0) {
		return
	}
	t.dataRuleError = ""
	var drafts []mysqlDataRuleDraft
	if t.dataFilterColumnDraft != "" && t.dataFilterColumnDraft != "选择字段" {
		drafts = append(drafts, mysqlDataRuleDraft{Column: t.dataFilterColumnDraft, Mode: t.dataFilterModeDraft, Value: t.dataFilterValueDraft})
	}
	drafts = append(drafts, t.dataFilterExtraDraft...)
	filters := make([]connections.MySQLDataFilterRule, 0, len(drafts))
	for _, draft := range drafts {
		valid := false
		for _, col := range t.dataColumns {
			if col.ID == draft.Column {
				valid = true
				break
			}
		}
		if !valid {
			t.dataRuleError = "筛选字段不存在或未选择"
			return
		}
		mode := mysqlFilterMode(draft.Mode)
		if mode == "" || len(draft.Value) > 4096 {
			t.dataRuleError = "筛选条件无效或过长"
			return
		}
		filters = append(filters, connections.MySQLDataFilterRule{Column: draft.Column, Mode: mode, Value: draft.Value})
	}
	if len(drafts) == 0 && len(t.dataSortDraft) == 0 && (t.dataFilterColumnDraft == "" || t.dataFilterColumnDraft == "选择字段") {
		t.dataRuleError = "请先选择筛选字段，或添加排序规则"
		return
	}
	sortRules := make([]connections.MySQLDataSortRule, 0, len(t.dataSortDraft))
	used := map[string]bool{}
	for _, draft := range t.dataSortDraft {
		found := false
		for _, col := range t.dataColumns {
			if col.ID == draft.Column && col.Sortable {
				found = true
				break
			}
		}
		if !found || used[draft.Column] {
			t.dataRuleError = "排序字段不可用或重复"
			return
		}
		used[draft.Column] = true
		sortRules = append(sortRules, connections.MySQLDataSortRule{Column: draft.Column, Descending: draft.Direction == "降序"})
	}
	if len(filters) > 1 {
		t.dataFilterRules = filters
		t.dataFilterMode = ""
		t.dataFilterColumn = ""
		t.dataFilterValue = ""
	} else {
		t.dataFilterRules = nil
		t.dataFilterMode = ""
		t.dataFilterColumn = ""
		t.dataFilterValue = ""
		if len(filters) == 1 {
			t.dataFilterColumn = filters[0].Column
			t.dataFilterMode = filters[0].Mode
			t.dataFilterValue = filters[0].Value
		}
	}
	if len(sortRules) > 1 {
		t.dataSortRules = sortRules
		t.dataSortColumn = ""
		t.dataSortDescending = false
	} else {
		t.dataSortRules = nil
		if len(sortRules) == 1 {
			t.dataSortColumn = sortRules[0].Column
			t.dataSortDescending = sortRules[0].Descending
		} else {
			t.dataSortColumn = ""
			t.dataSortDescending = false
		}
	}
	t.dataSort = ui.SortOrder{Column: t.dataSortColumn, Descending: t.dataSortDescending}
	if len(sortRules) > 1 {
		t.dataSort = ui.SortOrder{Column: sortRules[0].Column, Descending: sortRules[0].Descending}
	}
	s.resetAndReloadMySQLData(services, t)
}

func mysqlDataRuleSummary(t *tab) string {
	if len(t.dataFilterRules) == 0 {
		return mysqlDataFilterStatus(t)
	}
	var parts []string
	for _, filter := range t.dataFilterRules {
		parts = append(parts, filter.Column+" "+strings.ToUpper(filter.Mode))
	}
	return strings.Join(parts, " AND ")
}
