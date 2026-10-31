package connectioneditor

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLGeneralAdvancedAndDatabaseControls(t *testing.T) {
	s := New(Dependencies{})
	if err := s.OpenNew(connections.MySQL, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, 900, 700)
	}, 900, 700)
	for _, label := range []string{"连接名称：", "主机：", "端口：", "用户名：", "密码：", "保存密码"} {
		if !tt.HasText(label) {
			t.Fatalf("General tab missing %q", label)
		}
	}
	for _, note := range []string{
		"密码仅在当前窗口内保留；安全保存将在密钥库接入后开放。",
		"支持严格证书验证的 TLS；未加密连接仅允许本机，请勿发送生产凭据。",
	} {
		if tt.HasText(note) {
			t.Fatalf("General tab shows implementation note %q", note)
		}
	}
	for _, page := range []struct {
		name     string
		controls []string
	}{
		{"高级", []string{"客户端驱动程序：", "保持连接间隔（秒）：", "连接超时（秒）：", "初始查询：", "兼容性"}},
		{"数据库", []string{"使用自定义数据库列表", "自动打开"}},
	} {
		if err := tt.Click(page.name); err != nil {
			t.Fatal(err)
		}
		for _, label := range page.controls {
			if !tt.HasText(label) {
				t.Fatalf("%s tab missing %q", page.name, label)
			}
		}
	}
}

func assertThemePixel(t *testing.T, tt *ui.Tester, x, y int, want ui.Color) {
	t.Helper()
	got := tt.Image().RGBAAt(x, y)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Fatalf("editor pixel (%d,%d) RGB=%02x%02x%02x; expected NyaDB %02x%02x%02x",
			x, y, got.R, got.G, got.B, want.R, want.G, want.B)
	}
}

func TestConnectionWindowUsesWorkbenchPaletteOnEveryTab(t *testing.T) {
	s := newTestEditor()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	var native ui.Theme
	tt := ui.NewTester(func(c *ui.Context) {
		s.ConnectionWindowView(c)
		native = *c.Theme()
	}, 900, 700)
	p := theme.PaletteFor(theme.CurrentMode(), true)
	if native.Surface != p.Panel || native.Border != p.InputBorder || native.Text != p.Text || native.TextMuted != p.Muted || native.Focus != p.Focus {
		t.Fatal("connection form diverged from the workbench theme")
	}

	// Samples are deliberately in empty chrome, away from text and borders.
	// These test real rendered colors, not just token identities.
	for _, name := range []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		if name == "常规" {
			assertThemePixel(t, tt, 360, 14, theme.PaletteFor(theme.CurrentMode(), true).Header)
		}
		assertThemePixel(t, tt, 2, 440, theme.PaletteFor(theme.CurrentMode(), true).Toolbar)
		assertThemePixel(t, tt, 440, 680, theme.PaletteFor(theme.CurrentMode(), true).Toolbar)
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

func TestConnectionEditorProfileSidebarUsesWorkbenchSelection(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	if !s.connectionProfilesVisible || !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("editor's native configuration sidebar is missing")
	}
	// Profile rows use the same selection token as other navigation lists.
	assertThemePixel(t, tt, 240, 49, theme.PaletteFor(theme.CurrentMode(), true).Selection)
	if err := tt.Click("切换连接配置文件侧栏"); err != nil {
		t.Fatal(err)
	}
	if s.connectionProfilesVisible {
		t.Fatal("sidebar toggle stopped working after palette unification")
	}
}

func TestNativeConnectionEditorSmallWindowScrollsBodyKeepsFooter(t *testing.T) {
	s := newTestEditor()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	for _, tab := range []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"} {
		if err := tt.Click(tab); err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{"保存连接", "取消连接向导", "上一步"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.Y < 477 || r.X+r.W > 720 || r.Y+r.H > 520 {
				t.Fatalf("%s displaced footer %q: %+v", tab, label, r)
			}
		}
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	before, ok := tt.Find("编辑初始查询")
	if !ok || before.H >= connectionFormBrowseSize {
		t.Fatalf("expected advanced options to extend below the scroll viewport: %+v", before)
	}
	tt.Scroll(620, 420, 0, 400)
	after, ok := tt.Find("编辑初始查询")
	if !ok || after.Y < 29 || after.Y+after.H > 477 || after.Y >= before.Y {
		t.Fatalf("scroll failed to expose lower advanced options: before=%+v after=%+v", before, after)
	}
	if err := tt.Click("编辑初始查询"); err != nil {
		t.Fatal(err)
	}
	if !s.mysqlInitialQueryOpen {
		t.Fatal("visible scrolled advanced option was not clickable")
	}
	for _, label := range []string{"初始查询 SQL 编辑器", "确认初始查询", "取消初始查询"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 520 {
			t.Fatalf("initial query modal clips %q: %+v", label, r)
		}
	}
	tt.Key(0, ui.KeyEscape)
	if s.mysqlInitialQueryOpen || !s.connectionDialog {
		t.Fatal("Escape must close only the nested query modal")
	}
	s.connectionSecret = "temporary-session-secret"
	tt.Key(0, ui.KeyEscape)
	if s.connectionDialog || s.connectionSecret != "" {
		t.Fatal("Escape must cancel the native editor and clear session credentials")
	}
}

func TestConnectionFooterRemainsReachableWithMaximumReadingSize(t *testing.T) {
	previous := theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMetrics(previous) })
	theme.SetMetrics(theme.MetricsPreferences{UIFontSize: 16, SQLFontSize: 24, TableDensity: theme.DensityRelaxed})
	s := newTestEditor()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	for _, label := range []string{"保存连接", "取消连接向导", "上一步", "测试当前 MySQL 配置"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 520 {
			t.Fatalf("large-text action clipped: %s %+v", label, r)
		}
	}
}

func TestNativeConnectionEditorNarrowProfileSidebarRemainsReachable(t *testing.T) {
	s, _, _, _ := editMySQLProfileTester(t)
	s.mysqlCompatibilityVisible = true
	s.connectionEditorTab = 6
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	if !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("small edit window lost configuration navigation")
	}
	before, ok := tt.Find("强制视图列出方法为：选项")
	if !ok {
		t.Fatal("compatibility controls missing")
	}
	tt.Scroll(690, 300, 160, 200)
	after, ok := tt.Find("强制视图列出方法为：选项")
	if !ok || after.X < 258 || after.X+after.W > 720 || after.Y < 29 || after.Y+after.H > 477 || after.X >= before.X {
		t.Fatalf("narrow edit form cannot scroll to its right-side controls: before=%+v after=%+v", before, after)
	}
	if err := tt.Click("切换连接配置文件侧栏"); err != nil {
		t.Fatal(err)
	}
	if s.connectionProfilesVisible {
		t.Fatal("minimum window footer cannot collapse configuration sidebar")
	}
}

func TestNativeConnectionWideFormsRemainScrollableBesideProfiles(t *testing.T) {
	s, _, _, _ := editMySQLProfileTester(t)
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	for _, page := range []struct {
		tab, field string
		width      float32
	}{
		{"SSL", "SSL 客户端密钥：", 285},
		{"SSH", "SSH 主机：", 314},
		{"HTTP", "HTTP 隧道网址：", 314},
	} {
		if err := tt.Click(page.tab); err != nil {
			t.Fatal(err)
		}
		tt.Scroll(690, 300, 200, 0)
		r, ok := tt.Find(page.field)
		if !ok || r.X < 258 || r.X+r.W > 720 || r.W != page.width {
			t.Fatalf("%s full-width form is clipped beside the profile list: %+v", page.tab, r)
		}
		footer, ok := tt.Find("取消连接向导")
		if !ok || footer.Y < 477 || footer.Y+footer.H > 520 {
			t.Fatalf("%s scrolling displaced Cancel: %+v", page.tab, footer)
		}
	}
}

func TestMySQLNotesNativeTextAreaAndTabSwitch(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("备注：*") || !tt.HasText("* 更改将应用于所有连接配置文件。") {
		t.Fatal("original note label and footnote are missing")
	}
	if err := tt.Click("连接备注"); err != nil {
		t.Fatal(err)
	}
	const notes = "第一行\nSecond line\n第三行"
	tt.Type(notes)
	if s.connectionDraft.Notes != notes {
		t.Fatalf("multiline note not edited by native TextArea: %q", s.connectionDraft.Notes)
	}
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Notes != notes {
		t.Fatal("switching tabs discarded the edit draft")
	}
}

func TestMySQLNotesValidationInEditor(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "too long notes"
	s.connectionDraft.Notes = strings.Repeat("x", 64*1024+1)
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("oversize notes should keep editor open and show validation error")
	}
}

func TestMySQLEditorValidationAndSecretNotInProfile(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.connectionError, "连接名称") {
		t.Fatalf("blank name must be rejected: %q", s.connectionError)
	}
	s.connectionDraft.Name = "localhost development"
	s.connectionSecret = "not persisted"
	if p, err := s.draftProfile(); err != nil || p.Name == "" {
		t.Fatalf("valid draft not accepted: %+v %v", p, err)
	}
	if err := tt.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if s.connectionSecret != "" || s.connectionDialog {
		t.Fatal("Cancel should clear password and close")
	}
}

func TestConnectionDraftValidation(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "test"
	s.connectionPort = "99999"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("invalid port accepted")
	}
	s.connectionPort = "3306"
	s.connectionDraft.Host = ""
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("empty host accepted")
	}
	s.connectionDraft.Host = "localhost"
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.SQLite)
	s.nextConnectionStep()
	s.connectionDraft.Name = "local"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("empty SQLite file accepted")
	}
	s.connectionDraft.Database = "/tmp/example.sqlite"
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
	s.connectionSecret = "secret"
	s.closeConnectionDialog()
	if s.connectionSecret != "" {
		t.Fatal("secret left after dialog")
	}
}
