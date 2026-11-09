package sqlworkspace

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func commandModifier() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}
func sqlBindings() []editor.Binding {
	mod := commandModifier()
	return []editor.Binding{{Mods: mod, Key: ui.KeyEnter, Command: editor.NoCommand}, {Mods: mod, Key: ui.KeyL, Command: editor.NoCommand}}
}
func (s *State) RunCurrent(invalidate func(), d *Document) {
	if d == nil || d.Editor == nil || d.closed || d.preview {
		return
	}
	if text := strings.TrimSpace(s.statementAtCaret(d)); text != "" {
		s.runStatement(invalidate, d, text)
	}
}
func (s *State) RunSelection(invalidate func(), d *Document) {
	if d == nil || d.Editor == nil || d.closed || d.preview {
		return
	}
	if text := strings.TrimSpace(d.Editor.SelectedText()); text != "" {
		s.runStatement(invalidate, d, text)
	}
}
func (s *State) RunAll(invalidate func(), d *Document) {
	if d == nil || d.Editor == nil || d.preview || d.closed || d.Loading() {
		return
	}
	text := strings.TrimSpace(d.Editor.Buffer.String())
	if text != "" {
		s.runStatement(invalidate, d, text)
	}
}
func (s *State) sqlKeyboard(c *ui.Context, root ui.Element, d *Document) {
	mod := commandModifier()
	active := func() bool { return d != nil && !d.closed && !d.preview && s.active(d) && !s.ai.open }
	root.OnShortcut(mod, ui.KeyEnter, func() {
		if active() {
			s.Run(c.Services().Invalidate, d)
		}
	})
	root.OnShortcut(0, ui.KeyF5, func() {
		if active() {
			s.RunAll(c.Services().Invalidate, d)
		}
	})
	root.OnShortcut(mod, ui.KeyS, func() {
		if active() {
			s.Save(d, false, nil)
		}
	})
	root.OnShortcut(mod|ui.Shift, ui.KeyS, func() {
		if active() {
			s.Save(d, true, nil)
		}
	})
	root.OnShortcut(mod|ui.Shift, ui.KeyI, func() {
		if active() && !d.Loading() {
			s.Format(d)
		}
	})
	root.OnShortcut(mod, ui.KeyL, func() {
		if active() {
			s.OpenHistory(0, s.history.openEntry)
		}
	})
	root.OnShortcut(0, ui.KeyEscape, func() {
		if active() && d.Loading() {
			s.RequestCancel(d)
		}
	})
}
