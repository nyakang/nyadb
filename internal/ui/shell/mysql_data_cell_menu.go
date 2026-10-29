package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) mysqlDataCellMenu(c *ui.Context, t *tab, m *ui.Menu, row, source int) {
	page := t.dataPage
	if page == nil || row < 0 || row >= len(page.Rows) {
		return
	}
	names, rows, _ := mysqlDataSelectedPreview(t)
	empty := len(names) == 0 || len(rows) == 0
	if m.Item(locales.Text("复制")).Shortcut(ui.Ctrl, ui.KeyC).Disabled(empty).Chosen() {
		s.copyMySQLDataSelection(c, t)
	}
	if m.Item(locales.Text("复制字段名称")).Disabled(empty).Chosen() {
		c.WriteClipboard(mysqlDataDelimitedPreview(names, nil, '\t', true, false))
	}
	m.Submenu(locales.Text("复制为"), func(sub *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, sub) })
	m.Separator()
	if m.Item(locales.Text("查看字段信息")).Disabled(source < 0 || source >= len(page.Columns)).Chosen() {
		s.showMySQLFieldInspector(t)
	}
	if m.Item(locales.Text("显示/隐藏筛选 & 排序")).Checked(t.dataRulesExpanded).Chosen() {
		t.dataRulesExpanded = !t.dataRulesExpanded
	}
	if source >= 0 && source < len(page.Columns) && source < len(page.Rows[row]) {
		name, cell := page.Columns[source], page.Rows[row][source]
		ready := !t.dataLoading && t.dataProfileID == s.mysqlSessionProfileID && s.mysqlSessionStatus == "已连接"
		m.Submenu(locales.Text("筛选"), func(sub *ui.Menu) {
			kind := ""
			if source < len(page.ColumnKinds) {
				kind = strings.ToLower(page.ColumnKinds[source])
			}
			// Binary previews are hex strings, not the server values; NULL
			// predicates remain available even for binary or truncated fields.
			valueAvailable := mysqlDataCellFilterValueAvailable(kind, cell)
			for _, action := range []struct{ label, mode string }{
				{"字段 = 值", "equal"}, {"字段 != 值", "not_equal"}, {"字段包含值", "contains"},
				{"为空", "is_null"}, {"不为空", "is_not_null"},
			} {
				needsValue := action.mode != "is_null" && action.mode != "is_not_null"
				disabled := !ready || (needsValue && !valueAvailable) || (action.mode == "contains" && cell.Null)
				if sub.Item(locales.Text(action.label)).Disabled(disabled).Chosen() {
					s.applyMySQLDataCellFilter(c.Services(), t, name, action.mode, cell)
				}
			}
			sub.Separator()
			if sub.Item(locales.Text("自定义筛选")).Chosen() {
				t.dataRulesExpanded = true
			}
			if sub.Item(locales.Text("移除所有筛选")).Disabled(!ready).Chosen() {
				s.clearMySQLDataFilter(c.Services(), t)
			}
		})
		m.Submenu(locales.Text("排序"), func(sub *ui.Menu) {
			sortable := source < len(t.dataColumns) && t.dataColumns[source].Sortable
			for _, action := range []struct {
				label string
				desc  bool
			}{{"升序排序", false}, {"降序排序", true}} {
				if sub.Item(locales.Text(action.label)).Disabled(!ready || !sortable).Chosen() {
					s.applyMySQLDataCellSort(c.Services(), t, name, action.desc)
				}
			}
			sub.Separator()
			if sub.Item(locales.Text("移除所有排序")).Disabled(!ready || mysqlDataActiveSortCount(t) == 0).Chosen() {
				s.applyMySQLDataCellSort(c.Services(), t, "", false)
			}
		})
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(t.dataLoading || t.dataProfileID != s.mysqlSessionProfileID || s.mysqlSessionStatus != "已连接").Chosen() {
		s.resetAndReloadMySQLData(c.Services(), t)
	}
}

func mysqlDataCellFilterValueAvailable(kind string, cell connections.MySQLDataCell) bool {
	if cell.Truncated {
		return false
	}
	switch kind {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return false
	}
	return true
}

func (s *State) applyMySQLDataCellFilter(services ui.Services, t *tab, name, mode string, cell connections.MySQLDataCell) {
	if t.dataLoading {
		return
	}
	if cell.Null {
		switch mode {
		case "equal":
			mode = "is_null"
		case "not_equal":
			mode = "is_not_null"
		}
	}
	filters := append([]connections.MySQLDataFilterRule(nil), t.dataFilterRules...)
	if len(filters) == 0 && t.dataFilterMode != "" {
		filters = append(filters, connections.MySQLDataFilterRule{Column: t.dataFilterColumn, Mode: t.dataFilterMode, Value: t.dataFilterValue})
	}
	// Replace conditions on the clicked field and preserve other fields.
	kept := filters[:0]
	for _, rule := range filters {
		if rule.Column != name {
			kept = append(kept, rule)
		}
	}
	if len(kept) >= connections.MySQLDataMaxFilterRules {
		t.dataRuleError = locales.Text("筛选条件已达到上限")
		return
	}
	value := cell.Text
	if mode == "is_null" || mode == "is_not_null" {
		value = ""
	}
	t.dataFilterRules = append(kept, connections.MySQLDataFilterRule{Column: name, Mode: mode, Value: value})
	t.dataFilterColumn, t.dataFilterMode, t.dataFilterValue = "", "", ""
	t.dataFilterExtraDraft = nil
	for i, rule := range t.dataFilterRules {
		draft := mysqlDataRuleDraft{Column: rule.Column, Mode: mysqlDataFilterModeLabel(rule.Mode), Value: rule.Value}
		if i == 0 {
			t.dataFilterColumnDraft, t.dataFilterModeDraft, t.dataFilterValueDraft = draft.Column, draft.Mode, draft.Value
		} else {
			t.dataFilterExtraDraft = append(t.dataFilterExtraDraft, draft)
		}
	}
	t.dataRuleError = ""
	s.resetAndReloadMySQLData(services, t)
}

func mysqlDataFilterModeLabel(mode string) string {
	for _, label := range mysqlFilterModeChoices {
		if mysqlFilterMode(label) == mode {
			return label
		}
	}
	return "包含"
}

func (s *State) applyMySQLDataCellSort(services ui.Services, t *tab, name string, descending bool) {
	t.dataSortRules, t.dataSortDraft = nil, nil
	t.dataSortColumn, t.dataSortDescending = name, descending
	t.dataSort = ui.SortOrder{Column: name, Descending: descending}
	if name != "" {
		direction := "升序"
		if descending {
			direction = "降序"
		}
		t.dataSortDraft = []mysqlDataSortDraft{{Column: name, Direction: direction}}
	}
	t.dataRuleError = ""
	s.resetAndReloadMySQLData(services, t)
}
