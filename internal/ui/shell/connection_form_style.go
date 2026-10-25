package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The editor derives input and checkbox colors from the workbench palette.
// Shared by every connection tab and editable configuration profile.
var (
	connectionFieldBackground  = theme.Panel
	connectionFieldBorder      = theme.Border
	connectionFieldFocus       = theme.Blue
	connectionFieldText        = theme.Text
	connectionFieldMuted       = theme.Muted
	connectionCheckboxBorder   = theme.Border
	connectionCheckboxHover    = theme.Muted
	connectionCheckboxDisabled = theme.App
	connectionCheckColor       = theme.Text
	connectionCheckDisabled    = theme.Muted
)

const connectionCheckboxSize float32 = 14

// Keep MyGo's native text editing, selection, IME, popup keyboard handling,
// disabled semantics and focus border. Only its window-local theme is changed.
func applyConnectionEditorFormTheme(c *ui.Context) {
	theme.Apply(c)
	// Rebind tokens on each frame: the standalone editor survives mode changes.
	connectionFieldBackground = theme.Panel
	connectionFieldBorder = theme.Border
	if c.Theme().Dark {
		// In the observed dark editor, idle inputs blend into their dark
		// fill; the native input draws its accent border only when focused.
		connectionFieldBorder = theme.Panel
	}
	connectionFieldFocus = c.Theme().Focus
	connectionFieldText = theme.Text
	connectionFieldMuted = theme.Muted
	connectionCheckboxBorder = theme.Border
	connectionCheckboxHover = theme.Muted
	connectionCheckboxDisabled = theme.App
	connectionCheckColor = theme.Text
	connectionCheckDisabled = theme.Muted
	t := *c.Theme()
	t.Surface = connectionFieldBackground
	t.Border = connectionFieldBorder
	t.Text = connectionFieldText
	t.TextMuted = connectionFieldMuted
	t.Focus = connectionFieldFocus
	t.Accent = connectionFieldFocus // MyGo TextInput uses Accent for the focused border.
	c.SetTheme(&t)
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
	row := ui.CheckboxBase(c, checked).Label(locales.Text(label)).
		Height(connectionFormInputHeight).Gap(5).AlignItems(ui.Center).
		FocusRing(false).Disabled(disabled)
	row.Children(func() {
		fill := connectionFieldBackground
		border := connectionCheckboxBorder
		tick := connectionCheckColor
		ink := connectionFieldText
		if disabled {
			fill = connectionCheckboxDisabled
			border = connectionFieldBorder
			tick = connectionCheckDisabled
			// MyGo dims disabled subtrees; avoid double-dimming the label.
			ink = connectionFieldText
		} else if row.Hovered() {
			border = connectionCheckboxHover
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
						connectionFieldFocus, 3, 1)
				}
			})
		ui.Text(c, locales.Text(label)).FontSize(size).TextColor(ink)
	})
	return row
}
