package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func assertThemePixel(t *testing.T, tt *ui.Tester, x, y int, want ui.Color) {
	t.Helper()
	got := tt.Image().RGBAAt(x, y)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Fatalf("editor pixel (%d,%d) RGB=%02x%02x%02x; expected NyaDB %02x%02x%02x",
			x, y, got.R, got.G, got.B, want.R, want.G, want.B)
	}
}

func TestConnectionChooserSharesWorkbenchTheme(t *testing.T) {
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	assertThemePixel(t, tt, 888, 440, theme.PaletteFor(theme.CurrentMode(), true).App)
	if !tt.HasText("下一步") {
		t.Fatal("chooser lost its native navigation")
	}
}
