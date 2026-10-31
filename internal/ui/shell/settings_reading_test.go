package shell

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestReadingSettingsCommitBoundsRollbackAndReset(t *testing.T) {
	preserveAppearance(t)
	s := New()
	saved := map[string]string{}
	fail := false
	s.SetThemePreferenceSaver(func(key, value string) error {
		if fail {
			return errors.New("read only")
		}
		saved[key] = value
		return nil
	})
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); ui.Column(c).Fill().Children(func() { s.readingSettings(c) }) }, 800, 600)
	enter := func(label, value string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(value)
	}
	enter("设置界面字号", "16")
	if theme.CurrentMetrics().UIFontSize != 12 {
		t.Fatal("unconfirmed typing applied")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if theme.CurrentMetrics().UIFontSize != 16 || theme.CurrentMetrics().SQLFontSize != 13 {
		t.Fatalf("UI size was not committed independently: metrics=%+v input=%+v", theme.CurrentMetrics(), s.settings.uiSizeInput)
	}
	enter("设置 SQL 字号", "24")
	if err := tt.Click("宽松"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if theme.CurrentMetrics().SQLFontSize != 24 {
		t.Fatal("blur did not commit SQL size")
	}
	enter("设置 SQL 字号", "99")
	tt.Key(0, ui.KeyEnter)
	if theme.CurrentMetrics().SQLFontSize != 24 {
		t.Fatal("out of range size applied")
	}
	if err := tt.Click("宽松"); err != nil {
		t.Fatal(err)
	}
	if theme.DecodeMetrics(saved[theme.MetricsPreferenceKey]) != theme.CurrentMetrics() || theme.CurrentMetrics().TableDensity != theme.DensityRelaxed {
		t.Fatal("density not saved")
	}
	fail = true
	if err := tt.Click("标准"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMetrics().TableDensity != theme.DensityRelaxed || !s.settings.saveFailed {
		t.Fatal("failed persistence changed density")
	}
	fail = false
	for _, label := range []string{"恢复默认界面字号", "恢复默认 SQL 字号", "恢复默认行高"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if theme.CurrentMetrics() != theme.DefaultMetrics() || s.settings.saveFailed {
		t.Fatal("reset did not restore defaults")
	}
}

func TestReadingChangesKeepSQLHistorySearchAndBothWindows(t *testing.T) {
	preserveAppearance(t)
	s := New()
	s.openQuery()
	queryTab := s.currentTab()
	query := queryTab.query.Editor
	buffer := query.Buffer
	main := ui.NewTester(s.View, 1280, 800)
	query.Select(0, len(buffer.String()))
	query.Insert("SELECT id FROM users WHERE id = 42;")
	query.Find("id")
	main.Frame()
	query.Select(7, 9)
	text, selection := buffer.String(), query.Selection()
	matches, current := query.FindMatches()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	var childFont float32
	child := ui.NewTester(func(c *ui.Context) { s.ConnectionWindowView(c); childFont = c.Theme().FontSize }, 900, 700)
	for _, prefs := range []theme.MetricsPreferences{{UIFontSize: 16, SQLFontSize: 24, TableDensity: theme.DensityRelaxed}, {UIFontSize: 11, SQLFontSize: 10, TableDensity: theme.DensityStandard}, theme.DefaultMetrics()} {
		s.changeMetrics(prefs)
		main.Frame()
		child.Frame()
		if query.Font.Size != float32(prefs.SQLFontSize) || buffer != query.Buffer || buffer.String() != text || query.Selection() != selection || s.currentTab() != queryTab {
			t.Fatal("reading preferences changed SQL document state")
		}
		if n, i := query.FindMatches(); n != matches || i != current {
			t.Fatal("search state lost")
		}
		if !buffer.CanUndo() {
			t.Fatal("undo history cleared")
		}
		if childFont != float32(prefs.UIFontSize) {
			t.Fatal("connection window did not follow UI size")
		}
	}
	if !buffer.Undo() || buffer.String() == text {
		t.Fatal("SQL edit could not be undone after appearance changes")
	}
}

func TestReadingKeyboardFocusSurvivesGalleryReflow(t *testing.T) {
	preserveAppearance(t)
	s := New()
	s.openSettings()
	tt := ui.NewTester(s.View, 1280, 800)
	for i := 0; i < 50; i++ {
		if r, ok := tt.Find("设置界面字号"); ok && r.Y > 200 && r.Y+r.H < 700 {
			break
		}
		tt.Scroll(700, 650, 0, 100)
	}
	if err := tt.Click("设置界面字号"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("16")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if theme.CurrentMetrics().UIFontSize != 16 {
		t.Fatal("UI size did not apply")
	}
	// The gallery changed from two columns to one; the focused reading input
	// must keep its identity and its native tab order through that rebuild.
	// At the maximum, the disabled increase button is skipped.
	for i := 0; i < 3; i++ {
		tt.Key(0, ui.KeyTab)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("24")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if theme.CurrentMetrics().SQLFontSize != 24 {
		t.Fatal("gallery reflow lost focus before the SQL input")
	}
}
