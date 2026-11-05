package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

const CompactCheckboxSize float32 = 14

// CompactCheckbox centralizes the compact native checkbox appearance and focus feedback.
func CompactCheckbox(c *ui.Context, checked *bool, label string, disabled bool, size float32) ui.Element {
	palette := Colors(c)

	row := ui.CheckboxBase(c, checked).Label(locales.Text(label)).
		Height(RowHeight(c, ControlHeight)).Gap(5).AlignItems(ui.Center).
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
		} else if row.Pressed() {
			border = palette.Focus
		} else if row.Hovered() {
			border = palette.Muted
		}
		ui.Box(c).Size(CompactCheckboxSize, CompactCheckboxSize).
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
		ui.Text(c, locales.Text(label)).FontSize(Font(c, size)).TextColor(ink)
	})
	return row
}
