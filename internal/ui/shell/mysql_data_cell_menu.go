package shell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) mysqlDataCellMenu(c *ui.Context, t *tab, m *ui.Menu, row, source int) {
	page := t.dataPage
	if page == nil || row < 0 || row >= len(page.Rows) {
		return
	}
	ready := !t.dataLoading && !t.dataEdit.Busy && t.dataEdit.Uncertain == nil && t.dataProfileID == s.sessions.Current().ProfileID && s.sessions.Current().Status == "已连接"
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
					t.dataEdit.Err = err.Error()
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
			if sub.Item(format).Disabled(empty || t.dataExport.Busy).Chosen() {
				s.exportMySQLDataSelection(c, t, format)
			}
		}
	})
	m.Separator()
	if m.Item(locales.Text("查看字段信息")).Disabled(source < 0 || source >= len(page.Columns)).Chosen() {
		s.showMySQLFieldInspector(t)
	}
	if m.Item(locales.Text("显示/隐藏筛选 & 排序")).Checked(t.dataRulesExpanded).Chosen() {
		t.dataRulesExpanded = !t.dataRulesExpanded
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
				t.dataRulesExpanded = true
			}
			if sub.Item(locales.Text("移除筛选")).Disabled(!ready || !mysqlDataHasFieldFilter(t, name)).Chosen() {
				s.removeMySQLDataFieldFilter(c.Services(), t, name)
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
	if m.Item(locales.Text("移除所有筛选 & 排序")).Disabled(!ready).Chosen() {
		s.clearMySQLDataCellRules(c.Services(), t)
	}
	m.Separator()
	m.Submenu(locales.Text("缩放"), func(sub *ui.Menu) {
		zoom := t.dataEdit.Zoom
		if zoom == 0 {
			zoom = 100
		}
		for _, percent := range []int{50, 75, 90, 100, 110, 125, 150, 200} {
			if sub.Item(fmt.Sprintf("%d%%", percent)).Checked(zoom == percent).Chosen() {
				t.dataEdit.Zoom = percent
			}
		}
	})
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil || t.dataProfileID != s.sessions.Current().ProfileID || s.sessions.Current().Status != "已连接").Chosen() {
		s.resetAndReloadMySQLData(c.Services(), t)
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

func (s *State) applyMySQLDataCellFilter(services ui.Services, t *tab, name, mode string, cell mysqlsvc.MySQLDataCell) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.applyMySQLDataCellFilter(next, t, name, mode, cell) }) {
		return
	}
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
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), t.dataFilterRules...)
	if len(filters) == 0 && t.dataFilterMode != "" {
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: t.dataFilterColumn, Mode: t.dataFilterMode, Value: t.dataFilterValue})
	}
	// Replace conditions on the clicked field and preserve other fields.
	kept := filters[:0]
	for _, rule := range filters {
		if rule.Column != name {
			kept = append(kept, rule)
		}
	}
	if len(kept) >= mysqlsvc.MySQLDataMaxFilterRules {
		t.dataRuleError = locales.Text("筛选条件已达到上限")
		return
	}
	value := cell.Text
	if mode == "is_null" || mode == "is_not_null" {
		value = ""
	}
	t.dataFilterRules = append(kept, mysqlsvc.MySQLDataFilterRule{Column: name, Mode: mode, Value: value})
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

func (s *State) applyMySQLDataCellSort(services ui.Services, t *tab, name string, descending bool) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.applyMySQLDataCellSort(next, t, name, descending) }) {
		return
	}
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

func mysqlDataCanDeleteSelection(t *tab, clicked int) bool {
	if t == nil || t.dataPage == nil {
		return false
	}
	for row := range t.dataPage.Rows {
		if t.dataSelection.Has(row) || (t.dataSelection.Len() == 0 && row == clicked) {
			// A record draft can be canceled without a server identity; existing records need one.
			if d := mysqlDataDraftForRow(t, row); d != nil && d.Kind == "insert" {
				continue
			}
			if d := mysqlDataDraftForRow(t, row); d != nil && d.Kind == "delete" {
				return false
			}
			if row >= len(t.dataPage.RowRefs) || t.dataPage.RowRefs[row] == nil || row >= len(t.dataPage.RowSnapshots) || t.dataPage.RowSnapshots[row] == nil {
				return false
			}
		}
	}
	return clicked >= 0 && clicked < len(t.dataPage.Rows)
}

func (s *State) clearMySQLDataCellRules(services ui.Services, t *tab) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.clearMySQLDataCellRules(next, t) }) {
		return
	}
	t.dataSortRules, t.dataSortDraft = nil, nil
	t.dataSortColumn, t.dataSortDescending = "", false
	t.dataSort = ui.SortOrder{}
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
func mysqlDataHasFieldFilter(t *tab, name string) bool {
	if t.dataFilterColumn == name && t.dataFilterMode != "" {
		return true
	}
	for _, rule := range t.dataFilterRules {
		if rule.Column == name {
			return true
		}
	}
	return false
}
func (s *State) removeMySQLDataFieldFilter(services ui.Services, t *tab, name string) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.removeMySQLDataFieldFilter(next, t, name) }) {
		return
	}
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), t.dataFilterRules...)
	if len(filters) == 0 && t.dataFilterMode != "" {
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: t.dataFilterColumn, Mode: t.dataFilterMode, Value: t.dataFilterValue})
	}
	kept := filters[:0]
	for _, rule := range filters {
		if rule.Column != name {
			kept = append(kept, rule)
		}
	}
	t.dataFilterRules = kept
	t.dataFilterColumn, t.dataFilterMode, t.dataFilterValue = "", "", ""
	t.dataFilterExtraDraft = nil
	t.dataFilterColumnDraft, t.dataFilterModeDraft, t.dataFilterValueDraft = "选择字段", "包含", ""
	for i, rule := range kept {
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
