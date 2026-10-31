package dataworkspace

import "slices"

// Column identities, rather than their positions, survive hiding and moving
// fields. All selection consumers use this same displayed order.
func mysqlDataSelectedColumns(t *Document) []string {
	if t == nil || t.page == nil {
		return nil
	}
	var visible []string
	for _, source := range mysqlDataPasteColumns(t) {
		visible = append(visible, t.page.Columns[source])
	}
	if t.grid.ColumnSelection != nil {
		var selected []string
		for _, name := range visible {
			if slices.Contains(t.grid.ColumnSelection, name) {
				selected = append(selected, name)
			}
		}
		return selected
	}
	if t.grid.Cells.All {
		return visible
	}
	a, b := slices.Index(visible, t.grid.Cells.Anchor), slices.Index(visible, t.grid.Cells.Cursor)
	if b < 0 {
		return nil
	}
	if a < 0 {
		a = b
	}
	return visible[min(a, b) : max(a, b)+1]
}

func mysqlDataClearColumnSelection(t *Document) {
	t.grid.ColumnSelection = nil
}

// Placeholder headers retain layout without exposing callbacks captured from
// the previous successful page or its view context.
func mysqlDataSuspendGridActions(t *Document) {
	t.grid.HeaderSelect = false
	t.grid.HeaderMenu = nil
	t.grid.HeaderActionMenu = nil
	t.grid.OnCellClick = nil
	t.grid.CellMenu = nil
	t.grid.Sort = nil
	t.grid.CellSelect = false
}
