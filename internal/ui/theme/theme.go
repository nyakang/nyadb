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
	t := *c.Theme()
	t.Dark = true
	t.Background = App
	t.Surface = Header
	t.SurfaceHover = ui.Hex("#474747")
	t.SurfacePressed = ui.Hex("#515151")
	t.Border = Border
	t.Text, t.TextMuted = Text, Muted
	t.Accent = Accent
	t.AccentHover = ui.Hex("#1688e3")
	t.AccentPressed = ui.Hex("#086ab5")
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = Accent.Alpha(0.45)
	t.Focus = Blue
	t.Inverse, t.InverseText = Header, Text
	t.Spacing = 3
	t.FontSize = 13
	t.Radius = 3
	c.SetTheme(&t)
}
