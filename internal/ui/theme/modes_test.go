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
	if _, dark := effectivePalette(true); !dark {
		t.Fatal("system dark not followed")
	}
	if _, dark := effectivePalette(false); dark {
		t.Fatal("system light not followed")
	}
}

func TestLightThemeRendersAReadableWorkbench(t *testing.T) {
	before := CurrentMode()
	defer SetMode(before)
	SetMode(ModeLight)
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		ui.Box(c).Fill().Background(App)
	}, 120, 80)
	px := tt.Image().RGBAAt(60, 40)
	if px.R != App.R || px.G != App.G || px.B != App.B {
		t.Fatalf("light background mismatch: %v vs %v", px, App)
	}
	if Text.R >= 120 || Muted.R >= 180 {
		t.Fatal("light palette text lacks contrast")
	}
	SetMode(ModeDark)
	if App.R >= 100 {
		t.Fatal("dark theme not restored")
	}
}
