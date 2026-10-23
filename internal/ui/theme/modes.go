package theme

import "github.com/egoist/mygo/ui"

// Mode is the user's preference. The original dark workbench stays the default.
type Mode string

const (
	ModeDark   Mode = "dark"
	ModeLight  Mode = "light"
	ModeSystem Mode = "system"
)

func ParseMode(value string) Mode {
	switch Mode(value) {
	case ModeLight, ModeSystem:
		return Mode(value)
	default:
		return ModeDark
	}
}

// Palette names are semantic, not tied to a particular operating system.
type Palette struct {
	App, Menu, Toolbar, Sidebar, Panel, Header, Border                          ui.Color
	Accent, Text, Muted, Blue, Gold, Green, Purple                              ui.Color
	SurfaceHover, SurfacePressed, AccentHover, AccentPressed, AccentText, Focus ui.Color
}

var darkPalette = Palette{
	App: ui.Hex("#242424"), Menu: ui.Hex("#393939"), Toolbar: ui.Hex("#3d3d3d"),
	Sidebar: ui.Hex("#282828"), Panel: ui.Hex("#292929"), Header: ui.Hex("#333333"),
	Border: ui.Hex("#464646"), Accent: ui.Hex("#0879d1"), Text: ui.Hex("#eeeeee"),
	Muted: ui.Hex("#a6a6a6"), Blue: ui.Hex("#56b6ff"), Gold: ui.Hex("#f4c550"),
	Green: ui.Hex("#4ac48a"), Purple: ui.Hex("#c584ed"),
	SurfaceHover: ui.Hex("#474747"), SurfacePressed: ui.Hex("#515151"),
	AccentHover: ui.Hex("#1688e3"), AccentPressed: ui.Hex("#086ab5"),
	AccentText: ui.Hex("#ffffff"), Focus: ui.Hex("#56b6ff"),
}

var lightPalette = Palette{
	App: ui.Hex("#ffffff"), Menu: ui.Hex("#f3f4f6"), Toolbar: ui.Hex("#f7f7f8"),
	Sidebar: ui.Hex("#f8f9fb"), Panel: ui.Hex("#ffffff"), Header: ui.Hex("#f0f1f3"),
	Border: ui.Hex("#d3d7dd"), Accent: ui.Hex("#0879d1"), Text: ui.Hex("#20242a"),
	Muted: ui.Hex("#606873"), Blue: ui.Hex("#0867af"), Gold: ui.Hex("#9a6300"),
	Green: ui.Hex("#137a4b"), Purple: ui.Hex("#7852a5"),
	SurfaceHover: ui.Hex("#e5e8ed"), SurfacePressed: ui.Hex("#d9dee5"),
	AccentHover: ui.Hex("#006bbb"), AccentPressed: ui.Hex("#005a9f"),
	AccentText: ui.Hex("#ffffff"), Focus: ui.Hex("#0879d1"),
}

// MyGo renders on the UI thread. These legacy package-level tokens are kept
// until all workbench views have migrated to per-context palette access.
var selectedMode = ModeDark
var surfaceHover, surfacePressed, accentHover, accentPressed, accentText, focus ui.Color

func init() { setPalette(darkPalette) }

func setPalette(p Palette) {
	App, Menu, Toolbar, Sidebar, Panel, Header, Border =
		p.App, p.Menu, p.Toolbar, p.Sidebar, p.Panel, p.Header, p.Border
	Accent, Text, Muted, Blue, Gold, Green, Purple =
		p.Accent, p.Text, p.Muted, p.Blue, p.Gold, p.Green, p.Purple
	surfaceHover, surfacePressed = p.SurfaceHover, p.SurfacePressed
	accentHover, accentPressed, accentText, focus = p.AccentHover, p.AccentPressed, p.AccentText, p.Focus
}

func CurrentMode() Mode { return selectedMode }

// SetMode changes the selected mode; the host also updates MyGo's native
// appearance and requests redraws for both the workbench and modal.
func SetMode(mode Mode) {
	selectedMode = ParseMode(string(mode))
	if selectedMode == ModeLight {
		setPalette(lightPalette)
	} else if selectedMode == ModeDark {
		setPalette(darkPalette)
	}
}

func effectivePalette(systemDark bool) (Palette, bool) {
	dark := selectedMode == ModeDark || (selectedMode == ModeSystem && systemDark)
	if dark {
		return darkPalette, true
	}
	return lightPalette, false
}
