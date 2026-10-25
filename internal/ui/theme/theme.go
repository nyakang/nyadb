// Package theme defines the NyaDB desktop palette and original vector symbols.
package theme

import "github.com/egoist/mygo/ui"

var (
	App     = ui.Hex("#232629")
	Menu    = ui.Hex("#31363b")
	Toolbar = ui.Hex("#31363b")
	Sidebar = ui.Hex("#232629")
	Panel   = ui.Hex("#232629")
	Header  = ui.Hex("#31363b")
	Border  = ui.Hex("#454b50")
	Accent  = ui.Hex("#009dff")
	Text    = ui.Hex("#eeeeee")
	Muted   = ui.Hex("#979b9f")
	Blue    = ui.Hex("#009dff")
	Gold    = ui.Hex("#f4c550")
	Green   = ui.Hex("#4ac48a")
	Purple  = ui.Hex("#c584ed")

	// Selection belongs to lists and grids. ToolbarSelected intentionally has
	// much less emphasis so a mode button cannot look like a selected row.
	ToolbarSelected, Selection, Hover, Pressed ui.Color
)

// Apply keeps MyGo's native input behavior but gives every workbench widget
// the compact dark style used by the surrounding chrome.
func Apply(c *ui.Context) {
	p, dark := effectivePalette(c.Theme().Dark)
	setPalette(p)
	t := *c.Theme()
	t.Dark = dark
	t.Background = App
	t.Surface = Header
	t.SurfaceHover = surfaceHover
	t.SurfacePressed = surfacePressed
	t.Border = Border
	t.Text, t.TextMuted = Text, Muted
	t.Accent = Accent
	t.AccentHover = accentHover
	t.AccentPressed = accentPressed
	t.AccentText = accentText
	t.Selection = Selection
	t.Focus = focus
	if dark {
		t.Inverse, t.InverseText = Header, Text
	} else {
		// Keep tooltips and toasts distinct from the pale workspace chrome.
		t.Inverse, t.InverseText = ui.Hex("#30353b"), ui.Hex("#ffffff")
	}
	t.Spacing = 3
	t.FontSize = FontSize
	t.Radius = ControlRadius
	c.SetTheme(&t)
}
