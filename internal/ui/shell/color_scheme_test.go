package shell

import (
	"errors"
	"os"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func preserveAppearance(t *testing.T) {
	t.Helper()
	mode, scheme := theme.CurrentMode(), theme.CurrentScheme()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme) })
	theme.SetMode(theme.ModeDark)
	theme.SetScheme(theme.SchemeClassic)
}

func TestColorSchemesUpdateWindowsAndKeepSQLDocument(t *testing.T) {
	preserveAppearance(t)
	s := New()
	s.openQuery()
	queryTab := s.currentTab()
	query := queryTab.query.Editor
	query.Buffer.Set("SELECT name FROM users WHERE id = 42; -- keep this draft")
	buffer := query.Buffer
	main := ui.NewTester(s.View, 1280, 800)
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	child := ui.NewTester(s.ConnectionWindowView, 900, 700)
	for _, scheme := range theme.Schemes() {
		for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark, theme.ModeSystem} {
			s.changeColorScheme(scheme.ID)
			s.changeAppearance(mode)
			for _, systemDark := range []bool{false, true} {
				main.SetDark(systemDark)
				child.SetDark(systemDark)
				expected := theme.ResolveScheme(scheme.ID, mode, systemDark, ui.Preferences{})
				assertThemePixel(t, main, 2, 440, expected.Palette.Sidebar)
				assertThemePixel(t, child, 2, 440, expected.Palette.Toolbar)
				if query.Theme == nil || *query.Theme != expected.SQL {
					t.Fatalf("SQL palette drifted from %s %s", scheme.ID, mode)
				}
				if query.Buffer != buffer || buffer.String() != "SELECT name FROM users WHERE id = 42; -- keep this draft" || s.currentTab() != queryTab {
					t.Fatal("appearance switching replaced the SQL draft or active document")
				}
			}
		}
	}

}

func TestSchemeSettingsAndMenuSaveIndependentPreferences(t *testing.T) {
	preserveAppearance(t)
	s := New()
	saved := map[string]string{}
	s.SetThemePreferenceSaver(func(key, value string) error { saved[key] = value; return nil })
	s.openSettings()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("海洋 主题预览"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentScheme() != theme.SchemeOcean || theme.CurrentMode() != theme.ModeDark || saved[theme.SchemePreferenceKey] != "ocean" {
		t.Fatal("scheme card did not persist the family independently")
	}
	if err := tt.Click("浅色"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentScheme() != theme.SchemeOcean || saved[theme.ModePreferenceKey] != "light" {
		t.Fatal("mode switch reset the family")
	}
	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("主题", "配色方案", "森林"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if theme.CurrentScheme() != theme.SchemeForest || saved[theme.SchemePreferenceKey] != "forest" {
		t.Fatal("menu selection did not persist")
	}
	expected := theme.ResolveScheme(theme.SchemeForest, theme.ModeLight, false, ui.Preferences{})
	if s.settings.preview == nil || *s.settings.preview.Theme != expected.SQL {
		t.Fatal("settings SQL preview did not follow the selected scheme")
	}
}

func TestFailedThemeSaveKeepsSelectionAndCanRetry(t *testing.T) {
	preserveAppearance(t)
	s := New()
	fail, notified := true, 0
	s.SetThemePreferenceSaver(func(key, value string) error {
		if fail {
			return errors.New("disk is read-only")
		}
		return nil
	})
	s.SetColorSchemeHandler(func(theme.Scheme) { notified++ })
	s.SetAppearanceHandler(func(theme.Mode) { notified++ })
	s.openSettings()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("海洋 主题预览"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentScheme() != theme.SchemeClassic || notified != 0 || !tt.HasText("无法保存主题设置，请重试。") {
		t.Fatal("failed persistence changed the scheme or hid the error")
	}
	if err := tt.Click("浅色"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMode() != theme.ModeDark || notified != 0 {
		t.Fatal("failed persistence changed native appearance")
	}
	fail = false
	if err := tt.Click("海洋 主题预览"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("浅色"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentScheme() != theme.SchemeOcean || theme.CurrentMode() != theme.ModeLight || notified != 2 || s.settings.saveFailed {
		t.Fatal("retry did not apply the saved choices")
	}
}

func TestThemeGalleryVisualMatrix(t *testing.T) {
	output := os.Getenv("NYADB_THEME_CAPTURE")
	if output == "" {
		t.Skip("set NYADB_THEME_CAPTURE to capture the gallery and SQL workspace")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	preserveAppearance(t)
	for _, scheme := range theme.Schemes() {
		for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark} {
			theme.SetScheme(scheme.ID)
			theme.SetMode(mode)
			s := New()
			s.inspectorOn = false
			s.openSettings()
			tt := ui.NewTester(s.View, 1280, 800)
			prefix := string(scheme.ID) + "-" + string(mode)
			writeDomainCapture(t, output, prefix+"-gallery", tt)
			s.openQuery()
			s.currentTab().query.Editor.Buffer.Set(sqlThemePreview)
			tt.Frame()
			writeDomainCapture(t, output, prefix+"-sql", tt)
		}
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		theme.SetScheme(theme.SchemeOcean)
		theme.SetMode(theme.ModeSystem)
		s := New()
		s.openSettings()
		tt := ui.NewTester(s.View, 840, 560)
		writeDomainCapture(t, output, language+"-minimum-settings", tt)
		tt.Scroll(600, 490, 0, 1000)
		writeDomainCapture(t, output, language+"-minimum-preview", tt)
		tt.SetSize(1280, 900)
		tt.SetDark(true)
		tt.SetPreferences(ui.Preferences{HighContrast: true, TextScale: 1.5})
		writeDomainCapture(t, output, language+"-contrast-scaled-preview", tt)
	}
}
