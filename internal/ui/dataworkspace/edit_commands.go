package dataworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"slices"
)

func (s *State) gridEditTarget(t *Document) theme.EditTarget {
	return theme.EditTarget{ID: fmt.Sprintf("data/grid/%d", t.id), Valid: func() bool { return !t.closed && t.page != nil && t.edit.Editor == nil }, Enabled: func(command string) bool {
		switch command {
		case "undo":
			return s.canDraftUndo(t, false)
		case "redo":
			return s.canDraftUndo(t, true)
		case "copy":
			return mysqlDataHasSelectedPreview(t)
		case "select_all":
			return len(t.page.Rows) > 0
		case "paste":
			col := slices.Index(t.page.Columns, t.grid.Cells.Cursor)
			return !t.loading && !t.edit.Busy && col >= 0 && mysqlDataCanEdit(t, t.selectedRow, col)
		}
		return false
	}, Run: func(services ui.Services, command string) bool {
		switch command {
		case "undo":
			return s.undoDraft(t, false)
		case "redo":
			return s.undoDraft(t, true)
		case "copy":
			s.copyDataSelection(services, t)
		case "select_all":
			for row := range t.page.Rows {
				t.selection.Add(row)
			}
			t.grid.Cells.All = true
		case "paste":
			s.pasteDataSelection(services, t, t.selectedRow, slices.Index(t.page.Columns, t.grid.Cells.Cursor))
		default:
			return false
		}
		return true
	}}
}

func (s *State) Focus(d *Document) {
	if d != nil && !d.closed {
		d.grid.Handle.Focus()
	}
}
