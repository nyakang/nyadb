package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeConnectionChooserMinimumSizeAndSelection(t *testing.T) {
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 {
		t.Fatal("an unselected chooser must not advance")
	}
	for _, label := range []string{"选择 MySQL", "选择 PostgreSQL", "选择 SQLite", "取消连接向导", "下一步"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 520 {
			t.Fatalf("minimum chooser clips %q: %+v", label, r)
		}
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.SQLite {
		t.Fatal("minimum chooser did not navigate to the selected profile")
	}
}

func TestNativeConnectionEditorSmallWindowScrollsBodyKeepsFooter(t *testing.T) {
	s := New()
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
