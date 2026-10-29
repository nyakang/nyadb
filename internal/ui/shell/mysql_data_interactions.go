package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func mysqlSelectedDataCell(t *tab) (string, mysqlsvc.MySQLDataCell, bool) {
	if t == nil || t.dataPage == nil || t.dataSelectedRow < 0 ||
		t.dataSelectedRow >= len(t.dataPage.Rows) {
		return "", mysqlsvc.MySQLDataCell{}, false
	}
	name := t.dataState.Cells.Cursor
	for col, candidate := range t.dataPage.Columns {
		if name == candidate && col < len(t.dataPage.Rows[t.dataSelectedRow]) {
			return name, t.dataPage.Rows[t.dataSelectedRow][col], true
		}
	}
	return "", mysqlsvc.MySQLDataCell{}, false
}

func (s *State) applyMySQLDataFilter(services ui.Services, t *tab) {
	s.applyMySQLDataRules(services, t)
}
func (s *State) clearMySQLDataFilter(services ui.Services, t *tab) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.clearMySQLDataFilter(next, t) }) {
		return
	}
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
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.resetAndReloadMySQLData(next, t) }) {
		return
	}
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
	if t.dataEdit.Busy || t.dataEdit.LeaveOpen {
		return
	}
	requested := t.dataSort
	if (requested.Column != t.dataSortColumn || requested.Descending != t.dataSortDescending) && mysqlDataHasDraft(t) {
		if s.guardMySQLDataLeave(t, func(next ui.Services) { s.applyMySQLDataCellSort(next, t, requested.Column, requested.Descending) }) {
			t.dataSort = ui.SortOrder{Column: t.dataSortColumn, Descending: t.dataSortDescending}
			return
		}
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
