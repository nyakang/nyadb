package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// A field can be inspected before a row is selected. When no cell is active
// the first visible field is shown, matching the reference's initial panel.
// The full bit distinguishes live server metadata from legacy test-loader
// pages that only provide DATA_TYPE.
func mysqlSelectedDataField(t *tab) (connections.MySQLStructureColumn, bool, bool) {
	if t == nil || t.dataPage == nil {
		return connections.MySQLStructureColumn{}, false, false
	}
	page := t.dataPage
	name := t.dataState.Cells.Cursor
	if name == "" {
		for _, candidate := range page.Columns {
			if !t.dataHiddenColumns[candidate] {
				name = candidate
				break
			}
		}
	}
	for i, candidate := range page.Columns {
		if candidate != name || t.dataHiddenColumns[candidate] {
			continue
		}
		if i < len(page.ColumnMetadata) && page.ColumnMetadata[i].Name == name {
			return page.ColumnMetadata[i], true, true
		}
		field := connections.MySQLStructureColumn{Name: name}
		if i < len(page.ColumnKinds) {
			field.Type, field.DataType = page.ColumnKinds[i], page.ColumnKinds[i]
		}
		return field, true, false
	}
	return connections.MySQLStructureColumn{}, false, false
}

func mysqlSelectedDataCell(t *tab) (string, connections.MySQLDataCell, bool) {
	if t == nil || t.dataPage == nil || t.dataSelectedRow < 0 ||
		t.dataSelectedRow >= len(t.dataPage.Rows) {
		return "", connections.MySQLDataCell{}, false
	}
	name := t.dataState.Cells.Cursor
	for col, candidate := range t.dataPage.Columns {
		if name == candidate && col < len(t.dataPage.Rows[t.dataSelectedRow]) {
			return name, t.dataPage.Rows[t.dataSelectedRow][col], true
		}
	}
	return "", connections.MySQLDataCell{}, false
}

func mysqlDataCopyPreview(cell connections.MySQLDataCell) string {
	if cell.Null {
		return "NULL"
	}
	if cell.Truncated {
		return cell.Text + "\n[预览已截断；不是完整字段内容]"
	}
	return cell.Text
}

func (s *State) applyMySQLDataFilter(services ui.Services, t *tab) {
	s.applyMySQLDataRules(services, t)
}
func (s *State) clearMySQLDataFilter(services ui.Services, t *tab) {
	t.dataFilterColumn = ""
	t.dataFilterMode = ""
	t.dataFilterValue = ""
	t.dataFilterRules = nil
	t.dataFilterExtraDraft = nil
	t.dataRuleError = ""
	t.dataFilterColumnDraft = "选择字段"
	t.dataFilterValueDraft = ""
	s.resetAndReloadMySQLData(services, t)
}

func (s *State) resetAndReloadMySQLData(services ui.Services, t *tab) {
	if t == nil || t.kind != tabMySQLData {
		return
	}
	t.dataOffset = 0
	t.dataAfter = nil
	t.dataUseKeyset = true
	t.dataCursorHistory = nil
	t.dataPage = nil
	t.dataSelectedRow = -1
	s.cancelMySQLChunk(t)
	t.dataSelection.Clear()
	t.dataState.Cells = ui.ColumnRange{}
	s.loadMySQLData(services, t, true)
}

// SortOrder is controlled by MyGo's native draggable/resizable Table headers.
// Only columns marked Sortable in metadata may generate a remote sort query.
func (s *State) syncMySQLDataSort(services ui.Services, t *tab) {
	if t == nil || t.dataPage == nil {
		return
	}
	if len(t.dataSortRules) > 0 {
		first := t.dataSortRules[0]
		if t.dataSort.Column == first.Column && t.dataSort.Descending == first.Descending {
			return
		}
		t.dataSortRules = nil
		t.dataSortDraft = nil
	}
	if t.dataSort.Column == t.dataSortColumn && t.dataSort.Descending == t.dataSortDescending {
		return
	}
	if t.dataSort.Column != "" {
		valid := false
		for _, col := range t.dataColumns {
			if col.ID == t.dataSort.Column && col.Sortable {
				valid = true
				break
			}
		}
		if !valid {
			t.dataSort = ui.SortOrder{Column: t.dataSortColumn, Descending: t.dataSortDescending}
			return
		}
	}
	t.dataSortColumn = t.dataSort.Column
	t.dataSortDescending = t.dataSort.Descending
	s.resetAndReloadMySQLData(services, t)
}

func mysqlDataFilterStatus(t *tab) string {
	if t == nil {
		return ""
	}
	if len(t.dataFilterRules) > 0 {
		parts := make([]string, 0, len(t.dataFilterRules))
		for _, rule := range t.dataFilterRules {
			parts = append(parts, rule.Column+" "+rule.Mode)
		}
		return strings.Join(parts, " AND ")
	}
	if t.dataFilterMode == "" {
		return ""
	}
	label := "等于"
	if strings.EqualFold(t.dataFilterMode, "contains") {
		label = "包含"
	}
	return t.dataFilterColumn + " " + label + " " + t.dataFilterValue
}
