package theme

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestSnapshotsAndWindowsKeepIndependentAppearances(t *testing.T) {
	before := CurrentMode()
	t.Cleanup(func() { SetMode(before) })
	SetMode(ModeSystem)
	var light, dark Snapshot
	lightWindow := ui.NewTester(func(c *ui.Context) {
		light = Apply(c)
		ui.Box(c).Fill().Background(Colors(c).App)
	}, 80, 60)
	darkWindow := ui.NewTester(func(c *ui.Context) {
		dark = Apply(c)
		ui.Box(c).Fill().Background(Colors(c).App)
	}, 80, 60)
	darkWindow.SetDark(true)
	lightWindow.Frame()
	if light.Dark || !dark.Dark || light.Palette.App == dark.Palette.App {
		t.Fatal("windows shared a mutable palette")
	}
	saved := dark
	changed := Resolve(ModeLight, false, ui.Preferences{Accent: ui.Hex("#ffcc00"), TextScale: 1.5})
	changed.Palette.App = ui.Hex("#ff0000")
	SetMode(ModeLight)
	if dark != saved {
		t.Fatal("resolving another theme mutated an existing snapshot")
	}
	darkWindow.Frame()
	if dark.Dark {
		t.Fatal("forced light mode did not rebuild the dark window")
	}
	SetMode(ModeSystem)
	darkWindow.Frame()
	if !dark.Dark {
		t.Fatal("system appearance was lost after a forced theme")
	}
}

func TestThemeFollowsDesktopPreferencesWithoutCompounding(t *testing.T) {
	before := CurrentMode()
	t.Cleanup(func() { SetMode(before) })
	SetMode(ModeDark)
	var first, second ui.Theme
	var palette Palette
	var input ui.Theme
	var explicitFont, rowHeight float32
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		first = *c.Theme()
		Apply(c)
		second = *c.Theme()
		palette = Colors(c)
		input = *InputTheme(c)
		explicitFont, rowHeight = Font(c, SmallFontSize), RowHeight(c, ControlHeight)
		ui.Box(c).Fill().Background(palette.App)
	}, 100, 70)
	prefs := ui.Preferences{Accent: ui.Hex("#ffcc00"), HighContrast: true, TextScale: 1.5}
	tt.SetPreferences(prefs)
	if first != second {
		t.Fatal("reapplying the theme compounded system preferences")
	}
	if first.FontSize != 18 || explicitFont != 16.5 || rowHeight <= ControlHeight {
		t.Fatalf("text scaling lost: theme=%g explicit=%g row=%g", first.FontSize, explicitFont, rowHeight)
	}
	if first.Accent != prefs.Accent || first.Border == darkPalette.Border || first.TextMuted == darkPalette.Muted || first.Focus != palette.Text {
		t.Fatal("system accent or high contrast was overwritten")
	}
	if palette.InputBorder == palette.Panel {
		t.Fatal("dark inputs lost their high-contrast outlines")
	}
	for _, bg := range []ui.Color{input.Accent, input.AccentHover, input.AccentPressed} {
		if contrast(bg, input.AccentText) < 4.5 {
			t.Fatal("high contrast made form accent text unreadable")
		}
	}
	tt.SetPreferences(ui.Preferences{})
	if first.FontSize != FontSize || first.Border != darkPalette.Border || first.Accent != darkPalette.Accent {
		t.Fatal("clearing preferences did not restore the baseline")
	}
}

func TestPrimaryAndSelectionTextHaveReadableContrast(t *testing.T) {
	for _, mode := range []Mode{ModeLight, ModeDark} {
		for _, accent := range []ui.Color{ui.Transparent, ui.Hex("#ffcc00"), ui.Hex("#00ff00"), ui.Hex("#dd1111"), ui.Hex("#777777"), ui.Hex("#112233")} {
			p := Resolve(mode, false, ui.Preferences{Accent: accent}).Palette
			for _, bg := range []ui.Color{p.Accent, p.AccentHover, p.AccentPressed} {
				if got := contrast(bg, p.AccentText); got < 4.5 {
					t.Errorf("%s accent %v contrast %.3f", mode, accent, got)
				}
			}
			if got := contrast(p.Selection, p.SelectionText); got < 4.5 {
				t.Errorf("%s selection contrast %.3f", mode, got)
			}
		}
	}
}
