package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSelectedControlsRenderReadableColorPair(t *testing.T) {
	mode, scheme, metrics := CurrentMode(), CurrentScheme(), CurrentMetrics()
	t.Cleanup(func() { SetMode(mode); SetScheme(scheme); SetMetrics(metrics) })
	SetMetrics(DefaultMetrics())
	for _, scheme := range Schemes() {
		SetScheme(scheme.ID)
		for _, mode := range []Mode{ModeLight, ModeDark} {
			SetMode(mode)
			for _, accent := range []ui.Color{ui.Hex("#ffff00"), ui.Hex("#dd1111"), ui.Hex("#777777")} {
				var palette Palette
				tt := ui.NewTester(func(c *ui.Context) {
					Apply(c)
					palette = Colors(c)
					PageButton(c, "MMMMMMMM", true).MinWidth(200).FontSize(20)
				}, 220, 60)
				tt.SetPreferences(ui.Preferences{HighContrast: true, Accent: accent})
				if contrast(palette.SelectionText, palette.Selection) < 4.5 {
					t.Fatalf("unreadable selection: %s %s", scheme.ID, mode)
				}
				bg, fg := 0, 0
				im := tt.Image()
				for y := im.Bounds().Min.Y; y < im.Bounds().Max.Y; y++ {
					for x := im.Bounds().Min.X; x < im.Bounds().Max.X; x++ {
						r, g, b, _ := im.At(x, y).RGBA()
						if uint8(r>>8) == palette.Selection.R && uint8(g>>8) == palette.Selection.G && uint8(b>>8) == palette.Selection.B {
							bg++
						}
						if uint8(r>>8) == palette.SelectionText.R && uint8(g>>8) == palette.SelectionText.G && uint8(b>>8) == palette.SelectionText.B {
							fg++
						}
					}
				}
				if bg == 0 || fg == 0 {
					t.Fatalf("final widget lost the selection color pair: %s %s bg=%d fg=%d", scheme.ID, mode, bg, fg)
				}
			}
		}
	}
}

func TestNativeTabsKeyboardSelection(t *testing.T) {
	selected := 0
	tt := ui.NewTester(func(c *ui.Context) { Apply(c); Tabs(c, &selected, []string{"First", "Second", "Third"}, false) }, 400, 80)
	if err := tt.Click("First"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight)
	if selected != 1 {
		t.Fatal("Right did not change tab")
	}
	tt.Key(0, ui.KeyEnd)
	if selected != 2 {
		t.Fatal("End did not change tab")
	}
	tt.Key(0, ui.KeyHome)
	if selected != 0 {
		t.Fatal("Home did not change tab")
	}
}
