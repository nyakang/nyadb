package shell

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
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

func TestConnectionWindowUsesWorkbenchPaletteOnEveryTab(t *testing.T) {
	if connectionEditorSurface != theme.App || connectionEditorTabsBG != theme.Header ||
		connectionEditorBorder != theme.Border || connectionEditorButton != theme.Header ||
		connectionFieldText != theme.Text || connectionFieldMuted != theme.Muted ||
		connectionFieldFocus != theme.Blue {
		t.Fatal("connection editor has diverged from the shared workbench palette")
	}
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	// Samples are deliberately in empty chrome, away from text and borders.
	// These test real rendered colors, not just token identities.
	for _, name := range []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		if name == "常规" {
			assertThemePixel(t, tt, 360, 14, theme.Header)
		}
		assertThemePixel(t, tt, 880, 440, theme.App)
		assertThemePixel(t, tt, 440, 680, theme.Panel)
		if !tt.HasText("取消") || !tt.HasText("确定") {
			t.Fatalf("lost native footer actions on %s", name)
		}
		if dir := os.Getenv("NYADB_CONNECTION_THEME_CAPTURE"); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, fmt.Sprintf("mysql-%02d.png", s.connectionEditorTab))
			file, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(file, tt.Image()); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConnectionChooserSharesWorkbenchTheme(t *testing.T) {
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	tt := ui.NewTester(s.ConnectionWindowView, 902, 732)
	assertThemePixel(t, tt, 888, 440, theme.App)
	if !tt.HasText("下一步") {
		t.Fatal("chooser lost its native navigation")
	}
}

func TestConnectionEditorProfileSidebarUsesWorkbenchSelection(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	if !s.connectionProfilesVisible || !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("editor's native configuration sidebar is missing")
	}
	// The selected profile is a NyaDB accent row, not the former bright cyan.
	assertThemePixel(t, tt, 240, 49, theme.Accent)
	if err := tt.Click("切换连接配置文件侧栏"); err != nil {
		t.Fatal(err)
	}
	if s.connectionProfilesVisible {
		t.Fatal("sidebar toggle stopped working after palette unification")
	}
}
