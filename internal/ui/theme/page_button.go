package theme

import "github.com/egoist/mygo/ui"

// PageButton keeps active subpages visible without retaining native elements.
func PageButton(c *ui.Context, label string, active bool) ui.Element {
	b := ui.Button(c, label).Selected(active)
	if active {
		b.Background(Colors(c).Focus).Border(1, Colors(c).Focus)
	}
	return b
}
