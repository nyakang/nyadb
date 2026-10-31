package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestSchemesResolveIndependentlyOfAppearance(t *testing.T) {
	mode, scheme := CurrentMode(), CurrentScheme()
	t.Cleanup(func() { SetMode(mode); SetScheme(scheme) })
	seenLight, seenDark := map[ui.Color]bool{}, map[ui.Color]bool{}
	for _, item := range Schemes() {
		light := ResolveScheme(item.ID, ModeLight, true, ui.Preferences{})
		dark := ResolveScheme(item.ID, ModeDark, false, ui.Preferences{})
		if light.Dark || !dark.Dark || light.Palette.App == dark.Palette.App {
			t.Fatalf("%s did not provide distinct light/dark variants", item.ID)
		}
		if seenLight[light.Palette.App] || seenDark[dark.Palette.App] {
			t.Fatalf("%s only changed an accent, not the workspace", item.ID)
		}
		seenLight[light.Palette.App], seenDark[dark.Palette.App] = true, true
		if ResolveScheme(item.ID, ModeSystem, true, ui.Preferences{}) != dark ||
			ResolveScheme(item.ID, ModeSystem, false, ui.Preferences{}) != light {
			t.Fatalf("%s lost its color family when following the system", item.ID)
		}
		SetScheme(item.ID)
		SetMode(ModeLight)
		if CurrentScheme() != item.ID {
			t.Fatal("changing mode changed color scheme")
		}
	}
	if len(seenLight) != 5 || ParseScheme("") != SchemeClassic || ParseScheme("unknown") != SchemeClassic {
		t.Fatal("built-in schemes or legacy preference fallback missing")
	}
	items := Schemes()
	items[0].Name = "modified"
	if Schemes()[0].Name == "modified" {
		t.Fatal("caller mutated shared scheme metadata")
	}
}

func TestSchemeFrameDoesNotChangeDuringSelectionOrWidgetOverride(t *testing.T) {
	mode, scheme := CurrentMode(), CurrentScheme()
	t.Cleanup(func() { SetMode(mode); SetScheme(scheme) })
	SetMode(ModeSystem)
	SetScheme(SchemeOcean)
	var before, after Snapshot
	var palette Palette
	switchDuringBuild := false
	tt := ui.NewTester(func(c *ui.Context) {
		before = Apply(c)
		if switchDuringBuild {
			SetScheme(SchemeRose)
		}
		local := *c.Theme()
		local.Dark = !local.Dark
		local.Accent = ui.Hex("#ff0000")
		c.SetTheme(&local)
		after, palette = Frame(c), Colors(c)
		ui.Box(c).Fill().Background(palette.App)
	}, 100, 80)
	switchDuringBuild = true
	tt.Frame()
	if before != after || before.Palette != palette || after.Scheme != SchemeOcean {
		t.Fatal("a click or widget override changed a partially built frame")
	}
	switchDuringBuild = false
	tt.SetDark(true)
	if after.Scheme != SchemeRose || !after.Dark || after.SQL.Background != after.Palette.App {
		t.Fatal("next frame did not update the entire color family")
	}
}

func TestNamedSchemesKeepAccentsAndRespectAccessibility(t *testing.T) {
	for _, item := range Schemes()[1:] {
		for _, mode := range []Mode{ModeLight, ModeDark} {
			base := ResolveScheme(item.ID, mode, false, ui.Preferences{})
			accent := ResolveScheme(item.ID, mode, false, ui.Preferences{Accent: ui.Hex("#ff00ff")})
			if base != accent {
				t.Fatalf("desktop accent overrode %s %s", item.ID, mode)
			}
			high := ResolveScheme(item.ID, mode, false, ui.Preferences{HighContrast: true, TextScale: 1.5})
			if high.TextScale != 1.5 || high.Palette.Focus != high.Palette.Text || high.Palette.InputBorder == high.Palette.Panel {
				t.Fatalf("%s %s discarded accessibility preferences", item.ID, mode)
			}
		}
	}
}

func TestColorFamiliesHaveReadableContentAndSelections(t *testing.T) {
	for _, item := range Schemes() {
		for _, mode := range []Mode{ModeLight, ModeDark} {
			s := ResolveScheme(item.ID, mode, false, ui.Preferences{})
			p := s.Palette
			check := func(label string, ink, background ui.Color, minimum float64) {
				t.Helper()
				if ratio := contrast(ink, background); ratio < minimum {
					t.Errorf("%s %s %s contrast %.2f < %.1f", item.ID, mode, label, ratio, minimum)
				}
			}
			for _, bg := range []ui.Color{p.App, p.Header, p.Sidebar} {
				check("text", p.Text, bg, 4.5)
				if item.ID != SchemeClassic {
					check("secondary text", p.Muted, bg, 4.5)
				}
			}
			for _, bg := range []ui.Color{p.Accent, p.AccentHover, p.AccentPressed} {
				check("button", p.AccentText, bg, 4.5)
			}
			check("grid selection", p.SelectionText, p.Selection, 4.5)
			check("tree selection", p.NavigationSelectionText, p.NavigationSelection, 4.5)
			check("pending changes", p.NoticeText, p.Notice, 4.5)
			check("chart", p.Chart, p.App, 3)
			if item.ID == SchemeClassic {
				continue // Classic retains its existing SQL appearance.
			}
			for _, bg := range []ui.Color{s.SQL.Background, s.SQL.ActiveLine, s.SQL.Selection} {
				for _, kind := range []editor.TokenKind{editor.TokenKeyword, editor.TokenFunction, editor.TokenString, editor.TokenNumber, editor.TokenComment, editor.TokenInvalid} {
					check("SQL token", s.SQL.TokenColor(kind), bg, 4.5)
				}
			}
		}
	}
}
