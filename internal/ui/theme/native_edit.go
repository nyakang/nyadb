package theme

import "github.com/egoist/mygo/ui"

// BindNativeEdit registers the native text control's stable window-local focus.
func BindNativeEdit(c *ui.Context, e ui.Element, id string, focus *string, register func(EditTarget), valid func() bool, writable, secret bool) ui.Element {
	handle := ui.Local(e, "native-edit", func() ui.Handle { return ui.Handle{} })
	e.Bind(handle)
	if focus != nil {
		e.FocusBind(focus, id)
	}
	if e.FocusWithin() && register != nil {
		register(EditTarget{ID: id, Handle: handle, Native: true, Valid: valid, Enabled: func(command string) bool {
			switch command {
			case "copy":
				return !secret
			case "select_all":
				return true
			case "cut":
				return writable && !secret
			case "paste", "undo", "redo":
				return writable
			}
			return false
		}})
	}
	return e
}
