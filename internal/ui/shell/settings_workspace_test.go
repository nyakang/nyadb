package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestAppearanceSettingsAndLiveSQLPreview(t *testing.T) {
	previous := theme.CurrentMode()
	defer theme.SetMode(previous)
	theme.SetMode(theme.ModeDark)
	s := New()
	s.openQuery()
	query := s.currentTab().editor
	tt := ui.NewTester(s.View, 1280, 800)
	if query.Theme == nil || query.Theme.Background != theme.SQLTheme(&ui.Theme{Dark: true}).Background {
		t.Fatal("SQL query editor not using dark syntax palette")
	}
	s.openSettings()
	tt.Frame()
	if s.currentTab().kind != tabSettings || s.settingsPreview == nil {
		t.Fatal("settings tab or read-only native editor preview missing")
	}
	if !s.settingsPreview.ReadOnly || !tt.HasText("选择主题") {
		t.Fatal("settings preview must be accessible and read-only")
	}
	if err := tt.Click("浅色 主题预览"); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMode() != theme.ModeLight {
		t.Fatal("light preview failed to switch theme")
	}
	tt.Frame()
	if s.settingsPreview.Theme.Background != theme.SQLTheme(&ui.Theme{Dark: false}).Background {
		t.Fatal("SQL preview still has dark editor colors")
	}
	if !tt.HasText("SQL 语法高亮预览") {
		t.Fatal("SQL settings preview not rendered")
	}
	s.openSettings()
	if s.activeTab != 2 || len(s.tabs) != 3 {
		t.Fatal("settings should reuse existing tab")
	}
	s.activeTab = 1
	tt.Frame()
	if query.Theme == nil || query.Theme.Background != s.settingsPreview.Theme.Background {
		t.Fatal("open query and settings preview use different syntax themes")
	}
	s.changeAppearance(theme.ModeDark)
	tt.Frame()
	if query.Theme.Background != theme.SQLTheme(&ui.Theme{Dark: true}).Background {
		t.Fatal("existing SQL tab did not update without losing its buffer")
	}
}
