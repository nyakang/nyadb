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
	ui.NewTester(s.View, 1280, 800)
	if theme.Text.R > 100 || theme.App.R < 240 {
		t.Fatal("workbench did not apply light palette")
	}
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if connectionEditorSurface != theme.App || connectionFieldBackground != theme.Panel {
		t.Fatal("connection editor retained stale dark tokens")
	}
	assertThemePixel(t, tt, 880, 440, theme.App)
	s.changeAppearance(theme.ModeDark)
	ui.NewTester(s.ConnectionWindowView, 900, 700)
	if connectionEditorSurface != theme.App || theme.App.R >= 100 {
		t.Fatal("connection editor did not return to dark")
	}
}
