package shell

import "github.com/egoist/mygo/ui"

// Scoped to the connection editor. Applying these values to the workbench
// would change unrelated native widgets and is deliberately avoided.
var (
	connectionFieldBackground  = ui.Hex("#232629")
	connectionFieldBorder      = ui.Hex("#363c42")
	connectionFieldFocus       = ui.Hex("#457e9c")
	connectionFieldText        = ui.Hex("#d0d2d4")
	connectionFieldMuted       = ui.Hex("#858d93")
	connectionCheckboxBorder   = ui.Hex("#262a2e")
	connectionCheckboxHover    = ui.Hex("#707b83")
	connectionCheckboxDisabled = ui.Hex("#2a2d30")
	connectionCheckColor       = ui.Hex("#c7c9cb")
	connectionCheckDisabled    = ui.Hex("#797f83")
)

const connectionCheckboxSize float32 = 14

// Keep MyGo's native text editing, selection, IME, popup keyboard handling,
// disabled semantics and focus border. Only its window-local theme is changed.
func applyConnectionEditorFormTheme(c *ui.Context) {
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
	row := ui.CheckboxBase(c, checked).Label(label).
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
		ui.Text(c, label).FontSize(size).TextColor(ink)
	})
	return row
}
