// Package theme defines the NyaDB desktop palette and original vector symbols.
package theme

import "github.com/egoist/mygo/ui"

var (
	App     = ui.Hex("#242424")
	Menu    = ui.Hex("#393939")
	Toolbar = ui.Hex("#3d3d3d")
	Sidebar = ui.Hex("#282828")
	Panel   = ui.Hex("#292929")
	Header  = ui.Hex("#333333")
	Border  = ui.Hex("#464646")
	Accent  = ui.Hex("#0879d1")
	Text    = ui.Hex("#eeeeee")
	Muted   = ui.Hex("#a6a6a6")
	Blue    = ui.Hex("#56b6ff")
	Gold    = ui.Hex("#f4c550")
	Green   = ui.Hex("#4ac48a")
	Purple  = ui.Hex("#c584ed")
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
	t.Selection = Accent.Alpha(0.45)
	t.Focus = focus
	if dark {
		t.Inverse, t.InverseText = Header, Text
	} else {
		// Keep tooltips and toasts distinct from the pale workspace chrome.
		t.Inverse, t.InverseText = ui.Hex("#30353b"), ui.Hex("#ffffff")
	}
	t.Spacing = 3
	t.FontSize = 13
	t.Radius = 3
	c.SetTheme(&t)
}
