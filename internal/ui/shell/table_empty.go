package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The pinned MyGo version reuses a zero-row Table's ListState within a frame.
// Preserve the real column widths/order without registering a second list or
// inventing a data row. Once rows arrive the native Table resumes ownership.
func emptyTableView(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, message string) {
	palette := theme.Colors(c)

	ordered := make([]ui.TableColumn, 0, len(columns))
	seen := map[string]bool{}
	id := func(col ui.TableColumn) string {
		if col.ID != "" {
			return col.ID
		}
		return col.Title
	}
	for _, key := range state.Columns.Order {
		for _, col := range columns {
			if id(col) == key && !seen[key] && !col.Fixed {
				ordered = append(ordered, col)
				seen[key] = true
			}
		}
	}
	for _, col := range columns {
		if !seen[id(col)] && !col.Fixed {
			ordered = append(ordered, col)
		}
	}
	for i, col := range columns {
		if col.Fixed {
			ordered = append(ordered, ui.TableColumn{})
			copy(ordered[i+1:], ordered[i:])
			ordered[i] = col
		}
	}
	height := c.Theme().Space(8)
	for _, col := range columns {
		if col.Subtitle != "" {
			height = c.Theme().Space(15)
		}
	}
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.ScrollHorizontal(c).Height(height + 1).Children(func() {
			ui.Row(c).Height(height).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(palette.Border).Children(func() {
				for _, col := range ordered {
					width := col.Width
					if saved, ok := state.Columns.Widths[id(col)]; ok {
						width = saved
					}
					if width <= 0 {
						width = 120
					}
					ui.Column(c).Width(width).Height(height).Padding(3, 6).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(palette.Border).Children(func() {
						ui.Text(c, locales.Text(col.Title)).SingleLine().FontSize(theme.Font(c, theme.FontSize)).TextColor(palette.Muted).Bold()
						if col.Subtitle != "" {
							ui.Text(c, col.Subtitle).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted)
						}
					})
				}
			})
		})
		if message != "" {
			ui.Text(c, locales.Text(message)).Padding(8).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted)
		}
		ui.Box(c).Grow(1)
	})
}
