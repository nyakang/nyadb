package shell

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func TestConnectionCatalogLocalizedNativeWorkflow(t *testing.T) {
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
			s := New()
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
			for _, source := range []string{"连接筛选", "供应商筛选", "选择一个连接类型：", "下一步"} {
				if !tt.HasText(locales.Text(source)) {
					t.Errorf("missing %s chooser text for %q", language, source)
				}
			}
			if err := tt.Click(locales.Text("选择 ") + "MySQL"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(locales.Text("下一步")); err != nil {
				t.Fatal(err)
			}
			if s.connectionStep != 1 || s.connectionDraft.Kind != connections.MySQL {
				t.Fatal("localized chooser did not enter the MySQL editor")
			}
			if err := tt.Click(locales.Text("上一步")); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(locales.Text("取消连接向导")); err != nil {
				t.Fatal(err)
			}
			if s.connectionDialog {
				t.Fatal("localized cancel did not close the window")
			}
		})
	}
}

func TestConnectionCatalogResearchEntryPointPreservesWorkflow(t *testing.T) {
	s := New()
	dir := t.TempDir()
	if count, err := s.LoadLocalResearchIcons(dir); err != nil || count != 0 {
		t.Fatalf("missing research assets: %d, %v", count, err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "OV_64_MySQL_On.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := s.LoadLocalResearchIcons(dir); err != nil || count != 1 {
		t.Fatalf("forwarding valid research asset: %d, %v", count, err)
	}
	tt := ui.NewTester(s.View, 1280, 800)
	for _, label := range []string{"连接", "选择 MySQL", "下一步"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.MySQL {
		t.Fatal("research asset changed the connection workflow")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := s.LoadLocalResearchIcons(dir); err == nil || count != 0 {
		t.Fatalf("forwarding invalid research asset: %d, %v", count, err)
	}
	if count, err := s.LoadLocalResearchIcons(""); err != nil || count != 0 {
		t.Fatalf("disabling research icons: %d, %v", count, err)
	}
}

func TestConnectionCatalogUnsupportedType(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 MySQL"); err != nil {
		t.Fatal(err)
	}
	if s.connectionKind != connections.MySQL {
		t.Fatal("supported engine was not selected")
	}
	if tt.HasText("最近使用过的") {
		t.Fatal("selecting a card must not insert recents and move the catalog")
	}
	if err := tt.Click("选择 SQL Server（尚未支持）"); err != nil {
		t.Fatal(err)
	}
	if s.connectionKind != "" || !strings.Contains(s.connectionError, "尚未支持") {
		t.Fatalf("unsupported driver would proceed: %s / %q", s.connectionKind, s.connectionError)
	}
	s.nextConnectionStep()
	if s.connectionStep != 0 {
		t.Fatal("unsupported type advanced to configuration")
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.SQLite {
		t.Fatal("SQLite navigation broke")
	}
	s.openConnectionDialog()
	tt.Frame()
	if s.connectionCatalog.Selected().Name != "" || !tt.HasText("最近使用过的") {
		t.Fatal("reopening should clear selection but retain in-session recents")
	}
}

func TestConnectionCatalogNativeLayoutAndListMode(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"连接筛选", "供应商筛选", "全部", "云数据库", "搜索连接类型"} {
		if !tt.HasText(label) && label != "搜索连接类型" {
			t.Errorf("catalog section missing: %s", label)
		}
	}
	if tt.HasText("常用类型") {
		t.Fatal("the initial catalog must not duplicate types above the All section")
	}
	if err := tt.Click("列表视图"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("展开 阿里云") {
		t.Fatal("list switch did not change presentation")
	}
	if err := tt.Click("网格视图"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("展开 阿里云") {
		t.Fatal("grid switch did not change presentation")
	}
	if err := tt.Click("选择 MySQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Kind != connections.MySQL {
		t.Fatal("existing MySQL workflow regressed")
	}
}

func TestConnectionCatalogMinimumWindow(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 840, 560)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("连接筛选") || !tt.HasText("全部") {
		t.Fatal("catalog sections missing at minimum window size")
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.SQLite {
		t.Fatal("minimum window failed to enter native connection editor")
	}
}
