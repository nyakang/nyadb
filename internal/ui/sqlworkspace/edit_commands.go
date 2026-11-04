package sqlworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/gridview"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

var editCommands = map[string]editor.Command{"undo": editor.Undo, "redo": editor.Redo, "cut": editor.Cut, "copy": editor.Copy, "paste": editor.Paste, "select_all": editor.SelectAll, "find": editor.Find, "replace": editor.FindReplace}

func (s *State) registerEditor(d *Document, focused bool, handle *ui.Handle) {
	if s.deps.EditTarget == nil || d.Editor == nil || !focused {
		return
	}
	s.deps.EditTarget(theme.EditTarget{ID: fmt.Sprintf("sql/editor/%d", d.id), Handle: handle, Valid: func() bool { return !d.closed && d.Editor != nil && s.active(d) }, Enabled: func(command string) bool {
		switch command {
		case "undo":
			return !d.Editor.ReadOnly && d.Editor.Buffer.CanUndo()
		case "redo":
			return !d.Editor.ReadOnly && d.Editor.Buffer.CanRedo()
		case "copy":
			return d.Editor.SelectedText() != ""
		case "cut":
			return !d.Editor.ReadOnly && d.Editor.SelectedText() != ""
		case "paste", "replace":
			return !d.Editor.ReadOnly
		}
		_, ok := editCommands[command]
		return ok
	}, Run: func(_ ui.Services, command string) bool { return d.Editor.Run(editCommands[command]) }})
}

func (s *State) Focus(d *Document) {
	if d != nil && !d.closed {
		d.focusEditor = true
	}
}

func (s *State) registerResult(c *ui.Context, d *Document, table ui.Element) {
	handle := &d.grid.Handle
	if s.deps.EditFocus != nil {
		table.FocusBind(s.deps.EditFocus, fmt.Sprintf("sql/result/%d", d.id))
	}
	if s.deps.EditTarget == nil || !table.FocusWithin() {
		return
	}
	target := s.resultEditTarget(d)
	target.Handle = handle
	s.deps.EditTarget(target)
}
func (s *State) resultEditTarget(d *Document) theme.EditTarget {
	return theme.EditTarget{ID: fmt.Sprintf("sql/result/%d", d.id), Valid: func() bool { return !d.closed && s.active(d) }, Enabled: func(command string) bool {
		return command == "copy" && len(resultData(d, true).Rows) > 0 || command == "select_all" && len(selectedResult(d).Rows) > 0
	}, Run: func(services ui.Services, command string) bool {
		if command == "select_all" {
			for row := range selectedResult(d).Rows {
				d.selection.Add(row)
			}
			d.grid.Cells.All = true
			return true
		}
		services.WriteClipboard(gridview.Copy(resultData(d, true)))
		return true
	}}
}
