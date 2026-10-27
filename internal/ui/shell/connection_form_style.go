package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const connectionCheckboxSize float32 = 14

// Keep MyGo's native text editing, selection, IME, popup keyboard handling,
// disabled semantics and focus border. Only its window-local theme is changed.
func applyConnectionEditorFormTheme(c *ui.Context) {
	theme.Apply(c)
	c.SetTheme(theme.InputTheme(c))
}

// connectionFormCheckbox uses MyGo CheckboxBase: pointer/keyboard interaction,
// focus, checked state and accessibility are all native. Only its look differs
// from the workbench default (dark box, pale tick rather than blue fill).
func connectionFormCheckbox(c *ui.Context, checked *bool, label string, disabled bool) ui.Element {
	return connectionFormCheckboxSized(c, checked, label, disabled, 12)
}

func connectionFormCheckboxCompact(c *ui.Context, checked *bool, label string, disabled bool) ui.Element {
	return connectionFormCheckboxSized(c, checked, label, disabled, 11)
}

func connectionFormCheckboxSized(c *ui.Context, checked *bool, label string, disabled bool, size float32) ui.Element {
	palette := theme.Colors(c)

	row := ui.CheckboxBase(c, checked).Label(locales.Text(label)).
		Height(theme.RowHeight(c, connectionFormInputHeight)).Gap(5).AlignItems(ui.Center).
		FocusRing(false).Disabled(disabled)
	row.Children(func() {
		fill := palette.Panel
		border := palette.Border
		tick := palette.Text
		ink := palette.Text
		if disabled {
			fill = palette.App
			border = palette.InputBorder
			tick = palette.Muted
			// MyGo dims disabled subtrees; avoid double-dimming the label.
			ink = palette.Text
		} else if row.Hovered() {
			border = palette.Muted
		}
		ui.Box(c).Size(connectionCheckboxSize, connectionCheckboxSize).
			Radius(1).Background(fill).Border(1, border).
			DrawOver(func(p *ui.Painter, r ui.Rect) {
				if *checked {
					var mark ui.Path
					mark.MoveTo(r.X+r.W*0.22, r.Y+r.H*0.52).
						LineTo(r.X+r.W*0.42, r.Y+r.H*0.72).
						LineTo(r.X+r.W*0.79, r.Y+r.H*0.29)
					p.StrokePath(&mark, 1.6, tick)
				}
				if row.FocusVisible() {
					p.Stroke(ui.Rect{X: r.X - 2, Y: r.Y - 2, W: r.W + 4, H: r.H + 4},
						palette.Focus, 3, 1)
				}
			})
		ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, size)).TextColor(ink)
	})
	return row
}
