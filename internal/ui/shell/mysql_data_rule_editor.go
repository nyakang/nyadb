package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
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
	viewportWidth, viewportHeight := c.Size()
	narrow := viewportWidth < 1100
	ui.Column(c).Background(theme.Header).Padding(1, 6).Gap(2).Children(func() {
		ui.Row(c).Height(theme.TabHeight).Gap(5).AlignItems(ui.Center).Children(func() {
			label := "筛选 & 排序 ▸"
			if t.dataRulesExpanded {
				label = "筛选 & 排序 ▾"
			}
			if mysqlDataToolbarButton(c, label, locales.Text("筛选 & 排序"), "catalog-filter", false).Clicked() {
				t.dataRulesExpanded = !t.dataRulesExpanded
			}
			count := len(t.dataFilterRules)
			if count == 0 && t.dataFilterMode != "" {
				count = 1
			}
			ui.Text(c, fmt.Sprintf(locales.Text("筛选 %d · 排序 %d"), count, mysqlDataActiveSortCount(t))).FontSize(theme.SmallFontSize).TextColor(theme.Muted).SingleLine().MinWidth(0)
			if !narrow {
				ui.Toolbar(c, func() { s.mysqlDataCompactActions(c, t) }).Label(locales.Text("数据操作")).Grow(1).MinWidth(0).Padding(0).Gap(2)
			}
		})
		if narrow {
			ui.Toolbar(c, func() { s.mysqlDataCompactActions(c, t) }).Label(locales.Text("数据操作")).Height(theme.TabHeight).FillWidth().MinWidth(0).Padding(0).Gap(2)
		}
		if !t.dataRulesExpanded {
			if t.dataRuleError != "" {
				ui.Text(c, t.dataRuleError).SingleLine().FontSize(theme.SmallFontSize).TextColor(theme.Gold).Tooltip(t.dataRuleError)
			}
			return
		}
		// Rules scroll independently of the grid. Applying edits and paging stay
		// reachable even with every supported rule enabled at minimum size.
		height := min(float32(116+32*(len(t.dataFilterExtraDraft)+len(t.dataSortDraft))), max(float32(112), min(float32(240), viewportHeight*0.3)))
		ui.ScrollBoth(c).Label(locales.Text("筛选规则")).Height(height).FillWidth().Children(func() {
			ui.Column(c).MinWidth(530).Gap(3).Children(func() { s.mysqlDataRuleFields(c, t, page) })
		})
		ui.Row(c).Height(theme.CommandBarHeight).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, locales.Text("应用筛选 & 排序")).Label(locales.Text("应用筛选")).Height(theme.ControlHeight).Disabled(t.dataLoading).Clicked() {
				s.applyMySQLDataFilter(c.Services(), t)
			}
			message, color := mysqlDataFilterStatus(t), theme.Muted
			if t.dataRuleError != "" {
				message, color = t.dataRuleError, theme.Gold
			}
			ui.Text(c, message).SingleLine().FontSize(theme.SmallFontSize).TextColor(color).Grow(1).MinWidth(0).Tooltip(message)
		})
	})
}

func (s *State) mysqlDataRuleFields(c *ui.Context, t *tab, page *connections.MySQLDataPage) {
	choices := append([]string{"选择字段"}, page.Columns...)
	ui.Text(c, locales.Text("筛选  ·  所有已启用条件均满足 (AND)")).FontSize(11).TextColor(theme.Muted)
	ui.Row(c).Height(theme.CommandBarHeight).Gap(6).AlignItems(ui.Center).Children(func() {
		mysqlDataRuleSelect(c, &t.dataFilterColumnDraft, choices, mysqlDataColumnChoiceLabel).Height(theme.ControlHeight).Width(150)
		mysqlDataRuleSelect(c, &t.dataFilterModeDraft, mysqlFilterModeChoices, locales.Text).Width(110)
		if mysqlDataFilterModeNeedsValue(t.dataFilterModeDraft) {
			ui.TextInput(c, &t.dataFilterValueDraft).Label(locales.Text("数据筛选值")).Placeholder(locales.Text("筛选值")).Width(170)
		}
		ui.Box(c).Grow(1)
		if ui.Button(c, locales.Text("添加条件")).Label(locales.Text("添加筛选规则")).Disabled(len(t.dataFilterExtraDraft)+1 >= connections.MySQLDataMaxFilterRules).Clicked() {
			t.dataFilterExtraDraft = append(t.dataFilterExtraDraft, mysqlDataRuleDraft{Column: "选择字段", Mode: "包含"})
		}
	})
	for i := range t.dataFilterExtraDraft {
		i := i
		rule := &t.dataFilterExtraDraft[i]
		ui.Row(c).Height(theme.CommandBarHeight).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("且")).TextColor(theme.Muted).FontSize(11)
			mysqlDataRuleSelect(c, &rule.Column, choices, mysqlDataColumnChoiceLabel).Width(140)
			mysqlDataRuleSelect(c, &rule.Mode, mysqlFilterModeChoices, locales.Text).Width(110)
			if mysqlDataFilterModeNeedsValue(rule.Mode) {
				ui.TextInput(c, &rule.Value).Label(fmt.Sprintf(locales.Text("附加筛选值 %d"), i+1)).Placeholder(locales.Text("筛选值")).Width(170)
			}
			if ui.Button(c, locales.Text("移除")).Label(fmt.Sprintf(locales.Text("删除筛选规则 %d"), i+2)).Clicked() {
				t.dataFilterExtraDraft = append(t.dataFilterExtraDraft[:i], t.dataFilterExtraDraft[i+1:]...)
			}
		})
	}
	ui.Divider(c)
	ui.Row(c).Height(theme.ControlHeight).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.Text("排序方式 · 按规则优先级依次排序")).FontSize(11).TextColor(theme.Muted)
		ui.Box(c).Grow(1)
		if ui.Button(c, locales.Text("添加排序")).Disabled(len(t.dataSortDraft) >= connections.MySQLDataMaxSortRules).Clicked() {
			t.dataSortDraft = append(t.dataSortDraft, mysqlDataSortDraft{Column: "选择字段", Direction: "升序"})
		}
	})
	for i := range t.dataSortDraft {
		i := i
		rule := &t.dataSortDraft[i]
		ui.Row(c).Height(theme.CommandBarHeight).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("%d.", i+1)).FontSize(11).TextColor(theme.Muted)
			mysqlDataRuleSelect(c, &rule.Column, choices, mysqlDataColumnChoiceLabel).Width(165)
			mysqlDataRuleSelect(c, &rule.Direction, []string{"升序", "降序"}, locales.Text).Width(100)
			if ui.Button(c, locales.Text("移除")).Label(fmt.Sprintf(locales.Text("删除排序规则 %d"), i+1)).Clicked() {
				t.dataSortDraft = append(t.dataSortDraft[:i], t.dataSortDraft[i+1:]...)
			}
		})
	}
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
			t.dataRuleError = locales.Text("筛选字段不存在或未选择")
			return
		}
		mode := mysqlFilterMode(draft.Mode)
		if mode == "" || len(draft.Value) > 4096 {
			t.dataRuleError = locales.Text("筛选条件无效或过长")
			return
		}
		filters = append(filters, connections.MySQLDataFilterRule{Column: draft.Column, Mode: mode, Value: draft.Value})
	}
	if len(drafts) == 0 && len(t.dataSortDraft) == 0 && (t.dataFilterColumnDraft == "" || t.dataFilterColumnDraft == "选择字段") {
		t.dataRuleError = locales.Text("请先选择筛选字段，或添加排序规则")
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
			t.dataRuleError = locales.Text("排序字段不可用或重复")
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
