package dataworkspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strings"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func mysqlSelectedDataCell(t *Document) (string, mysqlsvc.MySQLDataCell, bool) {
	if t == nil || t.page == nil || t.selectedRow < 0 ||
		t.selectedRow >= len(t.page.Rows) {
		return "", mysqlsvc.MySQLDataCell{}, false
	}
	name := t.grid.Cells.Cursor
	for col, candidate := range t.page.Columns {
		if name == candidate && col < len(t.page.Rows[t.selectedRow]) {
			return name, t.page.Rows[t.selectedRow][col], true
		}
	}
	return "", mysqlsvc.MySQLDataCell{}, false
}

func (s *State) applyMySQLDataFilter(services ui.Services, t *Document) {
	s.applyMySQLDataRules(services, t)
}
func (s *State) clearMySQLDataFilter(services ui.Services, t *Document) {
	if s.GuardLeave(t, func(next ui.Services) { s.clearMySQLDataFilter(next, t) }) {
		return
	}
	t.filterColumn = ""
	t.filterMode = ""
	t.filterValue = ""
	t.filterRules = nil
	t.filterExtraDraft = nil
	t.ruleError = ""
	t.filterColumnDraft = "选择字段"
	t.filterValueDraft = ""
	s.Refresh(services, t)
}

func (s *State) Refresh(services ui.Services, t *Document) {
	if s.GuardLeave(t, func(next ui.Services) { s.Refresh(next, t) }) {
		return
	}
	if t == nil || t.closed {
		return
	}
	t.offset = 0
	t.after = nil
	t.useKeyset = true
	t.cursorHistory = nil
	t.page = nil
	t.selectedRow = -1
	s.cancelMySQLChunk(t)
	t.selection.Clear()
	t.grid.Cells = ui.ColumnRange{}
	s.Load(services, t, true)
}

// SortOrder is controlled by MyGo's native draggable/resizable Table headers.
// Only columns marked Sortable in metadata may generate a remote sort query.
func (s *State) syncMySQLDataSort(services ui.Services, t *Document) {
	if t == nil || t.page == nil {
		return
	}
	if t.edit.Busy || t.edit.LeaveOpen {
		return
	}
	requested := t.sort
	if (requested.Column != t.sortColumn || requested.Descending != t.sortDescending) && mysqlDataHasDraft(t) {
		if s.GuardLeave(t, func(next ui.Services) { s.applyMySQLDataCellSort(next, t, requested.Column, requested.Descending) }) {
			t.sort = ui.SortOrder{Column: t.sortColumn, Descending: t.sortDescending}
			return
		}
	}
	if len(t.sortRules) > 0 {
		first := t.sortRules[0]
		if t.sort.Column == first.Column && t.sort.Descending == first.Descending {
			return
		}
		t.sortRules = nil
		t.sortDraft = nil
	}
	if t.sort.Column == t.sortColumn && t.sort.Descending == t.sortDescending {
		return
	}
	if t.sort.Column != "" {
		valid := false
		for _, col := range t.columns {
			if col.ID == t.sort.Column && col.Sortable {
				valid = true
				break
			}
		}
		if !valid {
			t.sort = ui.SortOrder{Column: t.sortColumn, Descending: t.sortDescending}
			return
		}
	}
	t.sortColumn = t.sort.Column
	t.sortDescending = t.sort.Descending
	s.Refresh(services, t)
}

func mysqlDataFilterStatus(t *Document) string {
	if t == nil {
		return ""
	}
	if len(t.filterRules) > 0 {
		parts := make([]string, 0, len(t.filterRules))
		for _, rule := range t.filterRules {
			parts = append(parts, rule.Column+" "+rule.Mode)
		}
		return strings.Join(parts, " AND ")
	}
	if t.filterMode == "" {
		return ""
	}
	label := "等于"
	if strings.EqualFold(t.filterMode, "contains") {
		label = "包含"
	}
	return t.filterColumn + " " + label + " " + t.filterValue
}

type mysqlDataRuleDraft struct {
	Column, Mode, Value string
}
type mysqlDataSortDraft struct {
	Column, Direction string
}

var mysqlFilterModeChoices = []string{"包含", "不包含", "类似", "不类似", "精确匹配", "不等于", "小于", "大于", "为空", "不为空"}

func mysqlFilterMode(mode string) string {
	switch mode {
	case "包含":
		return "contains"
	case "不包含":
		return "not_contains"
	case "类似":
		return "like"
	case "不类似":
		return "not_like"
	case "小于":
		return "less"
	case "大于":
		return "greater"
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

func (s *State) mysqlDataRuleEditor(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage) {
	palette := theme.Colors(c)

	viewportWidth, viewportHeight := c.Size()
	narrow := viewportWidth < 1000
	ui.Column(c).Background(palette.Header).Padding(1, 6).Gap(2).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.TabHeight)).Gap(5).AlignItems(ui.Center).Children(func() {
			label := "筛选 & 排序 ▸"
			if t.rulesExpanded {
				label = "筛选 & 排序 ▾"
			}
			if mysqlDataToolbarButton(c, label, locales.Text("筛选 & 排序"), "catalog-filter", false).Clicked() {
				t.rulesExpanded = !t.rulesExpanded
			}
			count := len(t.filterRules)
			if count == 0 && t.filterMode != "" {
				count = 1
			}
			ui.Text(c, fmt.Sprintf(locales.Text("筛选 %d · 排序 %d"), count, mysqlDataActiveSortCount(t))).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted).SingleLine().MinWidth(0)
			if !narrow {
				ui.Toolbar(c, func() { s.mysqlDataCompactActions(c, t) }).Label(locales.Text("数据操作")).Grow(1).MinWidth(0).Padding(0).Gap(2)
			}
		})
		if narrow {
			ui.Toolbar(c, func() { s.mysqlDataCompactActions(c, t) }).Label(locales.Text("数据操作")).Height(theme.RowHeight(c, theme.TabHeight)).FillWidth().MinWidth(0).Padding(0).Gap(2)
		}
		if !t.rulesExpanded {
			if t.ruleError != "" {
				ui.Text(c, t.ruleError).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Error).Tooltip(t.ruleError)
			}
			return
		}
		// Rules scroll independently of the grid. Applying edits and paging stay
		// reachable even with every supported rule enabled at minimum size.
		height := min(float32(116+32*(len(t.filterExtraDraft)+len(t.sortDraft))), max(float32(112), min(float32(240), viewportHeight*0.3)))
		ui.ScrollBoth(c).Label(locales.Text("筛选规则")).Height(height).FillWidth().Children(func() {
			ui.Column(c).MinWidth(530).Gap(3).Children(func() { s.mysqlDataRuleFields(c, t, page) })
		})
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, locales.Text("应用筛选 & 排序")).Label(locales.Text("应用筛选")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(t.loading).Clicked() {
				s.applyMySQLDataFilter(c.Services(), t)
			}
			message, color := mysqlDataFilterStatus(t), palette.Muted
			if t.ruleError != "" {
				message, color = t.ruleError, palette.Gold
			}
			ui.Text(c, message).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(color).Grow(1).MinWidth(0).Tooltip(message)
		})
	})
}

func (s *State) mysqlDataRuleFields(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage) {
	palette := theme.Colors(c)

	choices := append([]string{"选择字段"}, page.Columns...)
	ui.Text(c, locales.Text("筛选  ·  所有已启用条件均满足 (AND)")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
	ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(6).AlignItems(ui.Center).Children(func() {
		mysqlDataRuleSelect(c, &t.filterColumnDraft, choices, mysqlDataColumnChoiceLabel).Height(theme.RowHeight(c, theme.ControlHeight)).Width(150)
		mysqlDataRuleSelect(c, &t.filterModeDraft, mysqlFilterModeChoices, locales.Text).Width(110)
		if mysqlDataFilterModeNeedsValue(t.filterModeDraft) {
			ui.TextInput(c, &t.filterValueDraft).Label(locales.Text("数据筛选值")).Placeholder(locales.Text("筛选值")).Width(170)
		}
		ui.Box(c).Grow(1)
		if ui.Button(c, locales.Text("添加条件")).Label(locales.Text("添加筛选规则")).Disabled(len(t.filterExtraDraft)+1 >= mysqlsvc.MySQLDataMaxFilterRules).Clicked() {
			t.filterExtraDraft = append(t.filterExtraDraft, mysqlDataRuleDraft{Column: "选择字段", Mode: "包含"})
		}
	})
	for i := range t.filterExtraDraft {
		i := i
		rule := &t.filterExtraDraft[i]
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("且")).TextColor(palette.Muted).FontSize(theme.Font(c, 11))
			mysqlDataRuleSelect(c, &rule.Column, choices, mysqlDataColumnChoiceLabel).Width(140)
			mysqlDataRuleSelect(c, &rule.Mode, mysqlFilterModeChoices, locales.Text).Width(110)
			if mysqlDataFilterModeNeedsValue(rule.Mode) {
				ui.TextInput(c, &rule.Value).Label(fmt.Sprintf(locales.Text("附加筛选值 %d"), i+1)).Placeholder(locales.Text("筛选值")).Width(170)
			}
			if ui.Button(c, locales.Text("移除")).Label(fmt.Sprintf(locales.Text("删除筛选规则 %d"), i+2)).Clicked() {
				t.filterExtraDraft = append(t.filterExtraDraft[:i], t.filterExtraDraft[i+1:]...)
			}
		})
	}
	ui.Divider(c)
	ui.Row(c).Height(theme.RowHeight(c, theme.ControlHeight)).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.Text("排序方式 · 按规则优先级依次排序")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		ui.Box(c).Grow(1)
		if ui.Button(c, locales.Text("添加排序")).Disabled(len(t.sortDraft) >= mysqlsvc.MySQLDataMaxSortRules).Clicked() {
			t.sortDraft = append(t.sortDraft, mysqlDataSortDraft{Column: "选择字段", Direction: "升序"})
		}
	})
	for i := range t.sortDraft {
		i := i
		rule := &t.sortDraft[i]
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("%d.", i+1)).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
			mysqlDataRuleSelect(c, &rule.Column, choices, mysqlDataColumnChoiceLabel).Width(165)
			mysqlDataRuleSelect(c, &rule.Direction, []string{"升序", "降序"}, locales.Text).Width(100)
			if ui.Button(c, locales.Text("移除")).Label(fmt.Sprintf(locales.Text("删除排序规则 %d"), i+1)).Clicked() {
				t.sortDraft = append(t.sortDraft[:i], t.sortDraft[i+1:]...)
			}
		})
	}
}

func mysqlDataActiveSortCount(t *Document) int {
	if len(t.sortRules) > 0 {
		return len(t.sortRules)
	}
	if t.sortColumn != "" {
		return 1
	}
	return 0
}

func (s *State) applyMySQLDataRules(services ui.Services, t *Document) {
	if s.GuardLeave(t, func(next ui.Services) { s.applyMySQLDataRules(next, t) }) {
		return
	}
	if t == nil || (t.page == nil && len(t.columns) == 0) {
		return
	}
	t.ruleError = ""
	var drafts []mysqlDataRuleDraft
	if t.filterColumnDraft != "" && t.filterColumnDraft != "选择字段" {
		drafts = append(drafts, mysqlDataRuleDraft{Column: t.filterColumnDraft, Mode: t.filterModeDraft, Value: t.filterValueDraft})
	}
	drafts = append(drafts, t.filterExtraDraft...)
	filters := make([]mysqlsvc.MySQLDataFilterRule, 0, len(drafts))
	for _, draft := range drafts {
		valid := false
		for _, col := range t.columns {
			if col.ID == draft.Column {
				valid = true
				break
			}
		}
		if !valid {
			t.ruleError = locales.Text("筛选字段不存在或未选择")
			return
		}
		mode := mysqlFilterMode(draft.Mode)
		if mode == "" || len(draft.Value) > 4096 {
			t.ruleError = locales.Text("筛选条件无效或过长")
			return
		}
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: draft.Column, Mode: mode, Value: draft.Value})
	}
	if len(drafts) == 0 && len(t.sortDraft) == 0 && (t.filterColumnDraft == "" || t.filterColumnDraft == "选择字段") {
		t.ruleError = locales.Text("请先选择筛选字段，或添加排序规则")
		return
	}
	sortRules := make([]mysqlsvc.MySQLDataSortRule, 0, len(t.sortDraft))
	used := map[string]bool{}
	for _, draft := range t.sortDraft {
		found := false
		for _, col := range t.columns {
			if col.ID == draft.Column && col.Sortable {
				found = true
				break
			}
		}
		if !found || used[draft.Column] {
			t.ruleError = locales.Text("排序字段不可用或重复")
			return
		}
		used[draft.Column] = true
		sortRules = append(sortRules, mysqlsvc.MySQLDataSortRule{Column: draft.Column, Descending: draft.Direction == "降序"})
	}
	if len(filters) > 1 {
		t.filterRules = filters
		t.filterMode = ""
		t.filterColumn = ""
		t.filterValue = ""
	} else {
		t.filterRules = nil
		t.filterMode = ""
		t.filterColumn = ""
		t.filterValue = ""
		if len(filters) == 1 {
			t.filterColumn = filters[0].Column
			t.filterMode = filters[0].Mode
			t.filterValue = filters[0].Value
		}
	}
	if len(sortRules) > 1 {
		t.sortRules = sortRules
		t.sortColumn = ""
		t.sortDescending = false
	} else {
		t.sortRules = nil
		if len(sortRules) == 1 {
			t.sortColumn = sortRules[0].Column
			t.sortDescending = sortRules[0].Descending
		} else {
			t.sortColumn = ""
			t.sortDescending = false
		}
	}
	t.sort = ui.SortOrder{Column: t.sortColumn, Descending: t.sortDescending}
	if len(sortRules) > 1 {
		t.sort = ui.SortOrder{Column: sortRules[0].Column, Descending: sortRules[0].Descending}
	}
	s.Refresh(services, t)
}

func mysqlDataRuleSummary(t *Document) string {
	if len(t.filterRules) == 0 {
		return mysqlDataFilterStatus(t)
	}
	var parts []string
	for _, filter := range t.filterRules {
		parts = append(parts, filter.Column+" "+strings.ToUpper(filter.Mode))
	}
	return strings.Join(parts, " AND ")
}

// Keep rule IDs and server column names separate from translated labels.
// SelectBase retains native popup focus and keyboard selection behavior.
func mysqlDataRuleSelect(c *ui.Context, value *string, choices []string, labels func(string) string) ui.Element {
	palette := theme.Colors(c)

	sel := ui.SelectBase(c, value)
	b := sel.Trigger.Height(theme.RowHeight(c, theme.ControlHeight)).Padding(3, 6).Gap(4).
		Background(palette.Header).Border(1, palette.Border).Radius(theme.ControlRadius)
	b.Children(func() {
		ui.Text(c, labels(*value)).SingleLine().Grow(1).MinWidth(0)
		ui.Text(c, "▾").TextColor(palette.Muted)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(3).Background(palette.Panel).Border(1, palette.Border).Radius(theme.ControlRadius)
		ui.Scroll(c).MaxHeight(240).Children(func() {
			ui.Column(c).Children(func() {
				for _, choice := range choices {
					item := sel.Item(choice).Padding(4, 6).Radius(theme.ControlRadius)
					if item.Highlighted() {
						item.Background(c.Theme().Accent).TextColor(c.Theme().AccentText)
					} else if choice == *value {
						item.Background(palette.Header)
					}
					item.Children(func() { ui.Text(c, labels(choice)).SingleLine() })
				}
			})
		})
	})
	return b
}

func mysqlDataColumnChoiceLabel(value string) string {
	if value == "选择字段" {
		return locales.Text("选择字段")
	}
	return value
}

func (s *State) mysqlDataCellMenu(c *ui.Context, t *Document, m *ui.Menu, row, source int) {
	page := t.page
	if page == nil || row < 0 || row >= len(page.Rows) {
		return
	}
	ready := !t.loading && !t.edit.Busy && t.edit.Uncertain == nil && t.profileID == s.session().ProfileID && s.session().Connected
	editable := ready && mysqlDataCanEdit(t, row, source)
	var column mysqlsvc.MySQLStructureColumn
	if source >= 0 && source < len(page.ColumnMetadata) {
		column = page.ColumnMetadata[source]
	}
	if m.Item(locales.Text("设置为空字符串")).Disabled(!editable || !mysqlDataEmptyValueAvailable(column)).Chosen() {
		s.setMySQLDataCell(c.Services(), t, row, source, mysqlDataEmptyValue(column))
	}
	nullable := source >= 0 && source < len(page.ColumnMetadata) && page.ColumnMetadata[source].Nullable
	if m.Item(locales.Text("设置为 NULL")).Disabled(!editable || !nullable).Chosen() {
		s.setMySQLDataCell(c.Services(), t, row, source, mysqlsvc.MySQLDataValue{Null: true})
	}
	m.Submenu(locales.Text("生成 UUID"), func(sub *ui.Menu) {
		for _, variant := range []struct {
			label          string
			upper, compact bool
		}{
			{"默认（xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx）", false, false},
			{"有连字符，大写", true, false}, {"无连字符，小写", false, true}, {"无连字符，大写", true, true},
		} {
			if sub.Item(locales.Text(variant.label)).Disabled(!editable || !mysqlDataUUIDAvailable(column, variant.compact)).Chosen() {
				value, err := mysqlDataUUID(variant.upper, variant.compact)
				if err != nil {
					t.edit.Err = err.Error()
					continue
				}
				s.setMySQLDataCell(c.Services(), t, row, source, mysqlsvc.MySQLDataValue{Text: value})
			}
		}
	})
	if m.Item(locales.Text("在单元格编辑器中编辑")).Disabled(!editable || mysqlDataSpatialField(column)).Chosen() {
		s.beginMySQLCellEdit(c.Services(), t, row, source, true)
	}
	m.Separator()
	if m.Item(locales.Text("添加记录")).Disabled(!ready || len(page.ColumnMetadata) == 0 || len(page.ColumnMetadata) != len(page.Columns)).Chosen() {
		s.addMySQLDataRecord(c.Services(), t)
	}
	if m.Item(locales.Text("删除记录")).Disabled(!ready || !mysqlDataCanDeleteSelection(t, row)).Chosen() {
		s.deleteMySQLDataRecords(c.Services(), t, row)
	}
	m.Separator()
	names, rows, _ := mysqlDataSelectedPreview(t)
	empty := len(names) == 0 || len(rows) == 0
	if m.Item(locales.Text("复制")).Shortcut(ui.Ctrl, ui.KeyC).Disabled(empty).Chosen() {
		s.copyMySQLDataSelection(c, t)
	}
	if m.Item(locales.Text("复制字段名称")).Disabled(empty).Chosen() {
		c.WriteClipboard(mysqlDataDelimitedPreview(names, nil, '\t', true, false))
	}
	m.Submenu(locales.Text("复制为"), func(sub *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, sub) })
	if m.Item(locales.Text("粘贴")).Shortcut(ui.Ctrl, ui.KeyV).Disabled(!editable || mysqlDataBinaryField(column) || mysqlDataSpatialField(column)).Chosen() {
		s.pasteMySQLDataSelection(c, t, row, source)
	}
	m.Submenu(locales.Text("保存数据为"), func(sub *ui.Menu) {
		for _, format := range []string{"CSV", "TSV", "JSON"} {
			if sub.Item(format).Disabled(empty || t.export.Busy).Chosen() {
				s.exportMySQLDataSelection(c, t, format)
			}
		}
	})
	m.Separator()
	if m.Item(locales.Text("查看字段信息")).Disabled(source < 0 || source >= len(page.Columns)).Chosen() {
		s.ShowField(t)
	}
	if m.Item(locales.Text("显示/隐藏筛选 & 排序")).Checked(t.rulesExpanded).Chosen() {
		t.rulesExpanded = !t.rulesExpanded
	}
	if source >= 0 && source < len(page.Columns) && source < len(page.Rows[row]) {
		name, cell := page.Columns[source], page.Rows[row][source]
		m.Submenu(locales.Text("筛选"), func(sub *ui.Menu) {
			kind := ""
			if source < len(page.ColumnKinds) {
				kind = strings.ToLower(page.ColumnKinds[source])
			}
			// Binary previews are hex strings, not the server values; NULL
			// predicates remain available even for binary or truncated fields.
			valueAvailable := mysqlDataCellFilterValueAvailable(kind, cell)
			for _, action := range []struct{ label, mode string }{
				{"字段 = 值", "equal"}, {"字段 != 值", "not_equal"}, {"字段类似值", "like"}, {"字段不类似值", "not_like"},
				{"字段 < 值", "less"}, {"字段 > 值", "greater"}, {"字段包含值", "contains"}, {"字段不包含值", "not_contains"},
				{"为空", "is_null"}, {"不为空", "is_not_null"},
			} {
				needsValue := action.mode != "is_null" && action.mode != "is_not_null"
				disabled := !ready || (needsValue && !valueAvailable) || ((action.mode == "contains" || action.mode == "not_contains" || action.mode == "like" || action.mode == "not_like") && (!mysqlDataContainsType(kind) || cell.Null)) || ((action.mode == "less" || action.mode == "greater") && cell.Null)
				if sub.Item(locales.Text(action.label)).Disabled(disabled).Chosen() {
					s.applyMySQLDataCellFilter(c.Services(), t, name, action.mode, cell)
				}
			}
			sub.Separator()
			if sub.Item(locales.Text("自定义筛选")).Chosen() {
				t.rulesExpanded = true
			}
			if sub.Item(locales.Text("移除筛选")).Disabled(!ready || !mysqlDataHasFieldFilter(t, name)).Chosen() {
				s.removeMySQLDataFieldFilter(c.Services(), t, name)
			}
			if sub.Item(locales.Text("移除所有筛选")).Disabled(!ready).Chosen() {
				s.clearMySQLDataFilter(c.Services(), t)
			}
		})
		m.Submenu(locales.Text("排序"), func(sub *ui.Menu) {
			sortable := source < len(t.columns) && t.columns[source].Sortable
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
	if m.Item(locales.Text("移除所有筛选 & 排序")).Disabled(!ready).Chosen() {
		s.clearMySQLDataCellRules(c.Services(), t)
	}
	m.Separator()
	m.Submenu(locales.Text("缩放"), func(sub *ui.Menu) {
		zoom := t.edit.Zoom
		if zoom == 0 {
			zoom = 100
		}
		for _, percent := range []int{50, 75, 90, 100, 110, 125, 150, 200} {
			if sub.Item(fmt.Sprintf("%d%%", percent)).Checked(zoom == percent).Chosen() {
				t.edit.Zoom = percent
			}
		}
	})
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.profileID != s.session().ProfileID || !s.session().Connected).Chosen() {
		s.Refresh(c.Services(), t)
	}
}

func mysqlDataCellFilterValueAvailable(kind string, cell mysqlsvc.MySQLDataCell) bool {
	if cell.Truncated {
		return false
	}
	switch kind {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return false
	}
	return true
}

func (s *State) applyMySQLDataCellFilter(services ui.Services, t *Document, name, mode string, cell mysqlsvc.MySQLDataCell) {
	if s.GuardLeave(t, func(next ui.Services) { s.applyMySQLDataCellFilter(next, t, name, mode, cell) }) {
		return
	}
	if t.loading {
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
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), t.filterRules...)
	if len(filters) == 0 && t.filterMode != "" {
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: t.filterColumn, Mode: t.filterMode, Value: t.filterValue})
	}
	// Replace conditions on the clicked field and preserve other fields.
	kept := filters[:0]
	for _, rule := range filters {
		if rule.Column != name {
			kept = append(kept, rule)
		}
	}
	if len(kept) >= mysqlsvc.MySQLDataMaxFilterRules {
		t.ruleError = locales.Text("筛选条件已达到上限")
		return
	}
	value := cell.Text
	if mode == "is_null" || mode == "is_not_null" {
		value = ""
	}
	t.filterRules = append(kept, mysqlsvc.MySQLDataFilterRule{Column: name, Mode: mode, Value: value})
	t.filterColumn, t.filterMode, t.filterValue = "", "", ""
	t.filterExtraDraft = nil
	for i, rule := range t.filterRules {
		draft := mysqlDataRuleDraft{Column: rule.Column, Mode: mysqlDataFilterModeLabel(rule.Mode), Value: rule.Value}
		if i == 0 {
			t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = draft.Column, draft.Mode, draft.Value
		} else {
			t.filterExtraDraft = append(t.filterExtraDraft, draft)
		}
	}
	t.ruleError = ""
	s.Refresh(services, t)
}

func mysqlDataFilterModeLabel(mode string) string {
	switch mode {
	case "like":
		return "类似"
	case "not_like":
		return "不类似"
	case "not_contains":
		return "不包含"
	case "less":
		return "小于"
	case "greater":
		return "大于"
	}
	for _, label := range mysqlFilterModeChoices {
		if mysqlFilterMode(label) == mode {
			return label
		}
	}
	return "包含"
}

func (s *State) applyMySQLDataCellSort(services ui.Services, t *Document, name string, descending bool) {
	if s.GuardLeave(t, func(next ui.Services) { s.applyMySQLDataCellSort(next, t, name, descending) }) {
		return
	}
	t.sortRules, t.sortDraft = nil, nil
	t.sortColumn, t.sortDescending = name, descending
	t.sort = ui.SortOrder{Column: name, Descending: descending}
	if name != "" {
		direction := "升序"
		if descending {
			direction = "降序"
		}
		t.sortDraft = []mysqlDataSortDraft{{Column: name, Direction: direction}}
	}
	t.ruleError = ""
	s.Refresh(services, t)
}

// UUIDs are generated locally from the system random source, never by a SQL expression.
func mysqlDataUUID(upper, compact bool) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	s := hex.EncodeToString(raw[:])
	if !compact {
		s = s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	}
	if upper {
		s = strings.ToUpper(s)
	}
	return s, nil
}

func mysqlDataCanDeleteSelection(t *Document, clicked int) bool {
	if t == nil || t.page == nil {
		return false
	}
	for row := range t.page.Rows {
		if t.selection.Has(row) || (t.selection.Len() == 0 && row == clicked) {
			// A record draft can be canceled without a server identity; existing records need one.
			if d := mysqlDataDraftForRow(t, row); d != nil && d.Kind == "insert" {
				continue
			}
			if d := mysqlDataDraftForRow(t, row); d != nil && d.Kind == "delete" {
				return false
			}
			if row >= len(t.page.RowRefs) || t.page.RowRefs[row] == nil || row >= len(t.page.RowSnapshots) || t.page.RowSnapshots[row] == nil {
				return false
			}
		}
	}
	return clicked >= 0 && clicked < len(t.page.Rows)
}

func (s *State) clearMySQLDataCellRules(services ui.Services, t *Document) {
	if s.GuardLeave(t, func(next ui.Services) { s.clearMySQLDataCellRules(next, t) }) {
		return
	}
	t.sortRules, t.sortDraft = nil, nil
	t.sortColumn, t.sortDescending = "", false
	t.sort = ui.SortOrder{}
	s.clearMySQLDataFilter(services, t)
}

func mysqlDataBinaryField(col mysqlsvc.MySQLStructureColumn) bool {
	switch strings.ToLower(col.DataType) {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit":
		return true
	}
	return false
}
func mysqlDataSpatialField(col mysqlsvc.MySQLStructureColumn) bool {
	switch strings.ToLower(col.DataType) {
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}
func mysqlDataEmptyValueAvailable(col mysqlsvc.MySQLStructureColumn) bool {
	switch strings.ToLower(col.DataType) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		return true
	}
	return mysqlDataBinaryField(col) && !strings.EqualFold(col.DataType, "bit")
}
func mysqlDataUUIDAvailable(col mysqlsvc.MySQLStructureColumn, compact bool) bool {
	// JSON, enum, numeric, temporal and binary fields cannot accept a textual
	// UUID directly. Fixed/variable strings must have room for the chosen form.
	switch strings.ToLower(col.DataType) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
	default:
		return false
	}
	length := int64(36)
	if compact {
		length = 32
	}
	return col.CharacterMaxLength == nil || *col.CharacterMaxLength >= length
}

func mysqlDataEmptyValue(col mysqlsvc.MySQLStructureColumn) mysqlsvc.MySQLDataValue {
	if mysqlDataBinaryField(col) {
		return mysqlsvc.MySQLDataValue{Binary: true, Bytes: []byte{}}
	}
	return mysqlsvc.MySQLDataValue{Text: ""}
}

func mysqlDataContainsType(kind string) bool {
	switch strings.ToLower(kind) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "json", "enum", "set":
		return true
	}
	return false
}
func mysqlDataHasFieldFilter(t *Document, name string) bool {
	if t.filterColumn == name && t.filterMode != "" {
		return true
	}
	for _, rule := range t.filterRules {
		if rule.Column == name {
			return true
		}
	}
	return false
}
func (s *State) removeMySQLDataFieldFilter(services ui.Services, t *Document, name string) {
	if s.GuardLeave(t, func(next ui.Services) { s.removeMySQLDataFieldFilter(next, t, name) }) {
		return
	}
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), t.filterRules...)
	if len(filters) == 0 && t.filterMode != "" {
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: t.filterColumn, Mode: t.filterMode, Value: t.filterValue})
	}
	kept := filters[:0]
	for _, rule := range filters {
		if rule.Column != name {
			kept = append(kept, rule)
		}
	}
	t.filterRules = kept
	t.filterColumn, t.filterMode, t.filterValue = "", "", ""
	t.filterExtraDraft = nil
	t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = "选择字段", "包含", ""
	for i, rule := range kept {
		draft := mysqlDataRuleDraft{Column: rule.Column, Mode: mysqlDataFilterModeLabel(rule.Mode), Value: rule.Value}
		if i == 0 {
			t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = draft.Column, draft.Mode, draft.Value
		} else {
			t.filterExtraDraft = append(t.filterExtraDraft, draft)
		}
	}
	t.ruleError = ""
	s.Refresh(services, t)
}
