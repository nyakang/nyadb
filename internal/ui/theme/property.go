package theme

import (
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/locales"
)

// The reference inspector stacks property labels above values, so long MySQL
// declarations and comments can wrap without shrinking the data grid.
func Property(c *ui.Context, label, value string) {
	palette := Colors(c)

	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, locales.Text(label)).FontSize(Font(c, 11)).TextColor(palette.Muted)
		ui.Text(c, value).FontSize(Font(c, 12))
	})
}

// PropertyRow keeps compact preview labels and values on one row.
func PropertyRow(c *ui.Context, label, value string) {
	palette := Colors(c)
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, locales.Text(label)).Width(62).FontSize(Font(c, 12)).TextColor(palette.Muted)
		ui.Text(c, value).FontSize(Font(c, 12)).SingleLine()
	})
}
