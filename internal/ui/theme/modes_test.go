package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestModeParsingAndSystemPalette(t *testing.T) {
	if ParseMode("") != ModeDark || ParseMode("invalid") != ModeDark {
		t.Fatal("unknown preference must use backwards-compatible dark theme")
	}
	if ParseMode("light") != ModeLight || ParseMode("system") != ModeSystem {
		t.Fatal("missing supported appearance mode")
	}
	before := CurrentMode()
	defer SetMode(before)
	SetMode(ModeSystem)
	if !Resolve(ModeSystem, true, ui.Preferences{}).Dark {
		t.Fatal("system dark not followed")
	}
	if Resolve(ModeSystem, false, ui.Preferences{}).Dark {
		t.Fatal("system light not followed")
	}
}

func TestLightThemeRendersAReadableWorkbench(t *testing.T) {
	before := CurrentMode()
	defer SetMode(before)
	SetMode(ModeLight)
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		ui.Box(c).Fill().Background(Colors(c).App)
	}, 120, 80)
	p := PaletteFor(ModeLight, false)
	px := tt.Image().RGBAAt(60, 40)
	if px.R != p.App.R || px.G != p.App.G || px.B != p.App.B {
		t.Fatalf("light background mismatch: %v vs %v", px, p.App)
	}
	if p.Text.R >= 120 || p.Muted.R >= 180 {
		t.Fatal("light palette text lacks contrast")
	}
	SetMode(ModeDark)
	if PaletteFor(CurrentMode(), true).App.R >= 100 {
		t.Fatal("dark theme not restored")
	}
}
