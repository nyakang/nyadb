package dataworkspace

// Record keys always remain the actual page row indices. A nonselectable
// statistical section can precede them without changing draft, copy, inspector
// or multiselection identities. Only the native list cursor uses display rows.
func mysqlDataSyncAnalysisGrid(t *Document) {
	if t.toolbar.gridOffset > 0 && t.toolbar.gridSelected != t.toolbar.gridLastSelected {
		t.selectedRow = mysqlDataRecordRow(t.toolbar.gridSelected, t.toolbar.gridOffset)
		t.toolbar.gridLastSelected = t.toolbar.gridSelected
	}
}

func mysqlDataRecordRow(row, offset int) int {
	if row < offset {
		return -1
	}
	return row - offset
}

func mysqlDataPrepareAnalysisGrid(t *Document) int {
	mysqlDataSyncAnalysisGrid(t)
	offset := 0
	if t.tools.analysisOpen && t.tools.analysisHeaders && t.tools.analysis != nil {
		offset = 1
	}
	t.toolbar.gridOffset = offset
	t.grid.Key = func(row int) any { return row - offset }
	t.grid.Header = nil
	t.grid.Selected = &t.selectedRow
	if offset > 0 {
		t.grid.Header = func(row int) bool { return row == 0 }
		t.toolbar.gridSelected = -1
		if t.selectedRow >= 0 {
			t.toolbar.gridSelected = t.selectedRow + offset
		}
		t.toolbar.gridLastSelected = t.toolbar.gridSelected
		t.grid.Selected = &t.toolbar.gridSelected
	}
	return offset
}

func mysqlDataScrollRecordIntoView(t *Document, row int) {
	t.grid.ScrollIntoView(row + t.toolbar.gridOffset)
}
