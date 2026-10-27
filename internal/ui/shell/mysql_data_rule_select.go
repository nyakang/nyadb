package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Keep rule IDs and server column names separate from translated labels.
// SelectBase retains native popup focus and keyboard selection behavior.
func mysqlDataRuleSelect(c *ui.Context, value *string, choices []string, labels func(string) string) ui.Element {
	palette := theme.Colors(c)

	sel := ui.SelectBase(c, value)
	b := sel.Trigger.Height(theme.RowHeight(c, theme.ControlHeight)).Padding(3, 6).Gap(4).
		Background(palette.Header).Border(1, palette.Border).Radius(theme.ControlRadius)
	b.Children(func() {
		ui.Text(c, labels(*value)).SingleLine().Grow(1).MinWidth(0)
		ui.Text(c, "▾").TextColor(palette.Muted)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(3).Background(palette.Panel).Border(1, palette.Border).Radius(theme.ControlRadius)
		ui.Scroll(c).MaxHeight(240).Children(func() {
			ui.Column(c).Children(func() {
				for _, choice := range choices {
					item := sel.Item(choice).Padding(4, 6).Radius(theme.ControlRadius)
					if item.Highlighted() {
						item.Background(c.Theme().Accent).TextColor(c.Theme().AccentText)
					} else if choice == *value {
						item.Background(palette.Header)
					}
					item.Children(func() { ui.Text(c, labels(choice)).SingleLine() })
				}
			})
		})
	})
	return b
}

func mysqlDataColumnChoiceLabel(value string) string {
	if value == "选择字段" {
		return locales.Text("选择字段")
	}
	return value
}
