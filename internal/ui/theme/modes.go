package theme

import (
	"github.com/egoist/mygo/ui"
	"sync/atomic"
)

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
	App, Menu, Toolbar, Sidebar, Panel, Header, Border                           ui.Color
	Accent, Text, Muted, Blue, Gold, Green, Purple                               ui.Color
	SelectionText, GridAlternate, GridNull, InputBorder, Error, Warning, Success ui.Color
	ToolbarSelected, Selection                                                   ui.Color
	SurfaceHover, SurfacePressed, AccentHover, AccentPressed, AccentText, Focus  ui.Color
}

var darkPalette = Palette{
	App: ui.Hex("#232629"), Menu: ui.Hex("#31363b"), Toolbar: ui.Hex("#31363b"),
	Sidebar: ui.Hex("#232629"), Panel: ui.Hex("#232629"), Header: ui.Hex("#31363b"),
	Border: ui.Hex("#454b50"), Accent: ui.Hex("#009dff"), Text: ui.Hex("#eeeeee"),
	Muted: ui.Hex("#979b9f"), Blue: ui.Hex("#009dff"), Gold: ui.Hex("#f4c550"),
	Green: ui.Hex("#4ac48a"), Purple: ui.Hex("#c584ed"),
	ToolbarSelected: ui.Hex("#3a3e42"), Selection: ui.Hex("#48b3eb"),
	SurfaceHover: ui.Hex("#3a4046"), SurfacePressed: ui.Hex("#424a51"),
	AccentHover: ui.Hex("#1688e3"), AccentPressed: ui.Hex("#0879d1"),
	AccentText: ui.Hex("#000000"), Focus: ui.Hex("#5ab7e6"),
	Error: ui.Hex("#ff8d8d"), Warning: ui.Hex("#f4c550"), Success: ui.Hex("#4ac48a"),
}

var lightPalette = Palette{
	App: ui.Hex("#ffffff"), Menu: ui.Hex("#f3f4f6"), Toolbar: ui.Hex("#f7f7f8"),
	Sidebar: ui.Hex("#f8f9fb"), Panel: ui.Hex("#ffffff"), Header: ui.Hex("#f0f1f3"),
	Border: ui.Hex("#d3d7dd"), Accent: ui.Hex("#0879d1"), Text: ui.Hex("#20242a"),
	Muted: ui.Hex("#606873"), Blue: ui.Hex("#0867af"), Gold: ui.Hex("#9a6300"),
	Green: ui.Hex("#137a4b"), Purple: ui.Hex("#7852a5"),
	ToolbarSelected: ui.Hex("#e2e5e8"), Selection: ui.Hex("#c5e1f4"),
	SurfaceHover: ui.Hex("#e5e8ed"), SurfacePressed: ui.Hex("#d9dee5"),
	AccentHover: ui.Hex("#006bbb"), AccentPressed: ui.Hex("#005a9f"),
	AccentText: ui.Hex("#ffffff"), Focus: ui.Hex("#0879d1"),
	Error: ui.Hex("#cf222e"), Warning: ui.Hex("#9a6300"), Success: ui.Hex("#137a4b"),
}

// Only the user's preference is process-wide; rendering colors are frame-local.
var selectedMode atomic.Uint32

func CurrentMode() Mode {
	switch selectedMode.Load() {
	case 1:
		return ModeLight
	case 2:
		return ModeSystem
	default:
		return ModeDark
	}
}

// SetMode updates the preference. The host sets the native appearance and
// redraws windows; existing frames keep their resolved colors until rebuilt.
func SetMode(mode Mode) {
	switch ParseMode(string(mode)) {
	case ModeLight:
		selectedMode.Store(1)
	case ModeSystem:
		selectedMode.Store(2)
	default:
		selectedMode.Store(0)
	}
}

// PaletteFor provides immutable read-only swatches for the settings preview.
// System mode is resolved through the current native appearance.
func PaletteFor(mode Mode, systemDark bool) Palette {
	return Resolve(mode, systemDark, ui.Preferences{}).Palette
}
