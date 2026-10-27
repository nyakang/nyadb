package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestAppearanceIsSharedByWorkbenchAndConnectionWindow(t *testing.T) {
	before := theme.CurrentMode()
	defer theme.SetMode(before)
	s := New()
	var changed theme.Mode
	s.SetAppearanceHandler(func(m theme.Mode) { changed = m })
	s.changeAppearance(theme.ModeLight)
	if changed != theme.ModeLight {
		t.Fatalf("host was not notified: %q", changed)
	}
	main := ui.NewTester(s.View, 1280, 800)
	assertThemePixel(t, main, 2, 440, theme.PaletteFor(theme.ModeLight, false).Sidebar)

	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	assertThemePixel(t, tt, 2, 440, theme.PaletteFor(theme.CurrentMode(), true).Toolbar)
	s.changeAppearance(theme.ModeDark)
	main.Frame()
	tt.Frame()
	assertThemePixel(t, main, 2, 440, theme.PaletteFor(theme.ModeDark, true).Sidebar)
	assertThemePixel(t, tt, 2, 440, theme.PaletteFor(theme.ModeDark, true).Toolbar)
}

func TestConnectionWindowFollowsContrastAndTextPreferences(t *testing.T) {
	before := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(before) })
	theme.SetMode(theme.ModeDark)
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	var native ui.Theme
	tt := ui.NewTester(func(c *ui.Context) {
		s.ConnectionWindowView(c)
		native = *c.Theme()
	}, 900, 700)
	tt.SetPreferences(ui.Preferences{HighContrast: true, TextScale: 1.5, Accent: ui.Hex("#ffcc00")})
	if native.FontSize != 18 || native.Border == native.Surface || native.Focus != native.Text {
		t.Fatal("connection form discarded desktop preferences")
	}
	field, ok := tt.Find("主机：")
	if !ok || field.H < 18 {
		t.Fatalf("enlarged host input missing or clipped: %v %v", field, ok)
	}
}
