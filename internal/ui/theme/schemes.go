package theme

import (
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Scheme chooses a color family, independently of the light/dark Mode.
// IDs are persisted; changing a display name must not change its ID.
type Scheme string

const (
	SchemeClassic Scheme = "classic"
	SchemeOcean   Scheme = "ocean"
	SchemeForest  Scheme = "forest"
	SchemeRose    Scheme = "rose"
	SchemeSand    Scheme = "sand"

	ModePreferenceKey   = "appearance"
	SchemePreferenceKey = "color_scheme"
)

type SchemeInfo struct {
	ID          Scheme
	Name        string // source-language label; the view translates it
	Description string
}

// Each variant defines actual surfaces and SQL inks, not just an accent.
// Interaction colors are derived centrally so every component agrees.
type schemeVariant struct {
	app, chrome, sidebar, header, border, text, muted, accent string
	keyword, function, str, number                            string
}

type schemeDefinition struct {
	info        SchemeInfo
	light, dark schemeVariant
}

var schemeDefinitions = [...]schemeDefinition{
	{info: SchemeInfo{SchemeClassic, "经典", "中性灰阶 · 经典蓝"}},
	{
		info: SchemeInfo{SchemeOcean, "海洋", "冷调蓝灰 · 海水蓝"},
		light: schemeVariant{
			"#f5f9ff", "#e5edf8", "#edf3fc", "#e8f0fb", "#bdcde1", "#20334d", "#536981", "#1266ad",
			"#135eac", "#6d4ac0", "#18705e", "#8b5519",
		},
		dark: schemeVariant{
			"#172536", "#21354c", "#1a2c40", "#23384e", "#3e5771", "#e0ecfc", "#a0b4cf", "#70baff",
			"#82c5ff", "#c9afff", "#91d8c4", "#f0c58d",
		},
	},
	{
		info: SchemeInfo{SchemeForest, "森林", "雾白与松绿 · 翡翠绿"},
		light: schemeVariant{
			"#f5faf6", "#e4eee7", "#ebf3ed", "#e7f0e9", "#b9cebe", "#233c2e", "#526c5a", "#22704e",
			"#216c4c", "#7251a1", "#45670e", "#90501d",
		},
		dark: schemeVariant{
			"#1b2923", "#293c31", "#203128", "#2b4035", "#466251", "#e2efe5", "#a5bfad", "#7bcc9e",
			"#8bdcaf", "#d2b6f2", "#c1d98a", "#eab98b",
		},
	},
	{
		info: SchemeInfo{SchemeRose, "玫瑰", "柔和灰紫 · 玫瑰粉"},
		light: schemeVariant{
			"#fdf7fb", "#f0e4ed", "#f6edf3", "#f2e8f0", "#d7bed0", "#442d40", "#795d72", "#a23c72",
			"#a03271", "#7650ae", "#287459", "#8e531b",
		},
		dark: schemeVariant{
			"#2d2230", "#413046", "#35283a", "#44334a", "#69516e", "#f3e4f0", "#c3a9c2", "#eea2ca",
			"#f2aed3", "#cbb4ff", "#a5d7bc", "#f0c29a",
		},
	},
	{
		info: SchemeInfo{SchemeSand, "暖沙", "暖白与褐灰 · 琥珀金"},
		light: schemeVariant{
			"#fcf8ef", "#eee6d7", "#f4edde", "#f1e9da", "#d0c2a8", "#443929", "#74644d", "#946019",
			"#8a591b", "#855283", "#4f6c32", "#a1432c",
		},
		dark: schemeVariant{
			"#2c2720", "#3e362a", "#342e24", "#443b2e", "#665a46", "#f0e7d6", "#c3b398", "#e5bd7b",
			"#ebc58c", "#d6b1d5", "#b9d59b", "#f0ad90",
		},
	},
}

// Schemes returns a fresh list in display order; callers cannot mutate the
// built-in definitions or another window's previews.
func Schemes() []SchemeInfo {
	items := make([]SchemeInfo, len(schemeDefinitions))
	for i, definition := range schemeDefinitions {
		items[i] = definition.info
	}
	return items
}

func schemeIndex(scheme Scheme) int {
	for i, definition := range schemeDefinitions {
		if definition.info.ID == scheme {
			return i
		}
	}
	return 0
}

func ParseScheme(value string) Scheme { return schemeDefinitions[schemeIndex(Scheme(value))].info.ID }

var selectedScheme atomic.Uint32

func CurrentScheme() Scheme { return schemeDefinitions[selectedScheme.Load()].info.ID }

func SetScheme(scheme Scheme) { selectedScheme.Store(uint32(schemeIndex(scheme))) }

func variantFor(scheme Scheme, dark bool) schemeVariant {
	definition := schemeDefinitions[schemeIndex(scheme)]
	if dark {
		return definition.dark
	}
	return definition.light
}

func schemePalette(scheme Scheme, dark bool) Palette {
	p := lightPalette
	if dark {
		p = darkPalette
	}
	if scheme != SchemeClassic {
		v := variantFor(scheme, dark)
		p.App, p.Panel = ui.Hex(v.app), ui.Hex(v.app)
		p.Menu, p.Toolbar = ui.Hex(v.chrome), ui.Hex(v.chrome)
		p.Sidebar, p.Header, p.Border = ui.Hex(v.sidebar), ui.Hex(v.header), ui.Hex(v.border)
		p.Text, p.Muted, p.Accent = ui.Hex(v.text), ui.Hex(v.muted), ui.Hex(v.accent)
		p.Blue, p.Focus = p.Accent, p.Accent
		p.SurfaceHover = p.Header.Mix(p.Text, 0.08)
		p.SurfacePressed = p.Header.Mix(p.Text, 0.14)
		p.ToolbarSelected = p.Header.Mix(p.Accent, 0.15)
		p.Selection = p.App.Mix(p.Accent, 0.28)
		p.AccentText = onColor(p.Accent)
		p.AccentHover = readableBackground(p.Accent.Mix(p.Text, 0.1), p.AccentText)
		p.AccentPressed = readableBackground(p.Accent.Mix(p.Text, 0.2), p.AccentText)
	}
	return p
}
