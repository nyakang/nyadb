package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

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
	if t == nil || t.kind != tabMySQLData || t.dataPage == nil {
		return
	}
	if t.dataFilterColumnDraft == "" || t.dataFilterColumnDraft == "选择字段" {
		t.dataErr = "请先选择筛选字段"
		return
	}
	found := false
	for _, name := range t.dataPage.Columns {
		if name == t.dataFilterColumnDraft {
			found = true
			break
		}
	}
	if !found {
		t.dataErr = "筛选字段不存在"
		return
	}
	if len(t.dataFilterValueDraft) > 4096 {
		t.dataErr = "筛选值过长"
		return
	}
	mode := "equal"
	if t.dataFilterModeDraft == "包含" {
		mode = "contains"
	}
	t.dataFilterColumn = t.dataFilterColumnDraft
	t.dataFilterMode = mode
	t.dataFilterValue = t.dataFilterValueDraft
	s.resetAndReloadMySQLData(services, t)
}

func (s *State) clearMySQLDataFilter(services ui.Services, t *tab) {
	t.dataFilterColumn = ""
	t.dataFilterMode = ""
	t.dataFilterValue = ""
	t.dataFilterColumnDraft = "选择字段"
	t.dataFilterValueDraft = ""
	s.resetAndReloadMySQLData(services, t)
}

func (s *State) resetAndReloadMySQLData(services ui.Services, t *tab) {
	if t == nil || t.kind != tabMySQLData {
		return
	}
	t.dataOffset = 0
	t.dataPage = nil
	t.dataSelectedRow = -1
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
	if t == nil || t.dataFilterMode == "" {
		return ""
	}
	label := "等于"
	if strings.EqualFold(t.dataFilterMode, "contains") {
		label = "包含"
	}
	return t.dataFilterColumn + " " + label + " " + t.dataFilterValue
}
