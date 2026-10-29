package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestInitialNavicatStyleLayout(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	for _, label := range []string{"我的连接", "对象", "新建查询", "信息", "搜索连接", "NyaDB"} {
		if !tt.HasText(label) {
			t.Errorf("initial view missing %q", label)
		}
	}
	if !s.inspectorOn || len(s.tabs) != 1 || s.currentTab().kind != tabObjects {
		t.Fatalf("unexpected initial layout state: inspector=%v tabs=%d", s.inspectorOn, len(s.tabs))
	}
}

func TestOpenQueryAndExecuteMock(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("新建查询"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabSQL || s.activeTab != 1 {
		t.Fatalf("new query not activated: %+v", s.tabs)
	}
	if s.currentTab().query.Editor.Buffer.Language().Name != "SQL" {
		t.Fatal("SQL editor did not activate SQL syntax")
	}
	if err := tt.Click("运行模拟查询"); err != nil {
		t.Fatal(err)
	}
	if s.sqlWorkspace.DemoRuns() != 1 || !strings.Contains(s.status, "没有访问数据库") {
		t.Fatalf("unexpected query status: %s", s.status)
	}
	if err := tt.Click("关闭 SQL 1"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 1 || s.activeTab != 0 || s.currentTab().kind != tabObjects {
		t.Fatalf("closing query did not restore Objects tab")
	}
}

func TestInspectorToggleAndObjectKinds(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("信息"); err != nil {
		t.Fatal(err)
	}
	if s.inspectorOn {
		t.Fatal("info toolbar should hide inspector")
	}
	if err := tt.Click("信息"); err != nil {
		t.Fatal(err)
	}
	if !s.inspectorOn {
		t.Fatal("info toolbar should reopen inspector")
	}
	if err := tt.Click("视图"); err != nil {
		t.Fatal(err)
	}
	if s.currentObjectKind() != "视图" || s.activeTab != 0 {
		t.Fatal("view action should activate filtered Objects tab")
	}
}

func TestSearchAndOpenMockTable(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("测试库"); err != nil {
		t.Fatal(err)
	}
	if s.preview.key != "测试库" {
		t.Fatalf("selected connection: %q", s.preview.key)
	}
	if !tt.HasText("customers") {
		t.Fatalf("mock table should be visible after selecting a connection")
	}
	if err := tt.Click("打开表"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabData {
		t.Fatal("opening table should show a Data tab")
	}
}

func TestSearchConnections(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("搜索连接"); err != nil {
		t.Fatal(err)
	}
	tt.Type("生产")
	if s.preview.search != "生产" {
		t.Fatalf("expected Chinese input, got %q", s.preview.search)
	}
	if !tt.HasText("生产库") || tt.HasText("测试库") {
		t.Fatalf("search did not filter the connection tree")
	}
}

func TestResponsivePaneDefaults(t *testing.T) {
	s := New()
	if s.sidebarPercent < 19 || s.sidebarPercent > 21 || s.centerPercent < 79 || s.centerPercent > 82 {
		t.Fatalf("unexpected initial pane proportions: %v/%v", s.sidebarPercent, s.centerPercent)
	}
	tt := ui.NewTester(s.View, 900, 600)
	tt.SetSize(1552, 966)
	if !tt.HasText("我的连接") || !tt.HasText("信息") {
		t.Fatal("panes lost their content after resizing")
	}
}

func TestSyntheticPreview(t *testing.T) {
	if PreviewColumns != 50 || PreviewRows != 100_000 {
		t.Fatal("unexpected fixture dimensions")
	}
	if got := PreviewCell(9, 0); got != "10" {
		t.Fatalf("row ID: %q", got)
	}
	if got := PreviewCell(9, 1); got != "customer_000010" {
		t.Fatalf("name: %q", got)
	}
	if got := PreviewCell(9, 2); got != "user000010@example.test" {
		t.Fatalf("email: %q", got)
	}
	if got := demoChildren("customers"); got != nil {
		t.Fatalf("table should be a leaf, got %v", got)
	}
}

func TestTabIDsAndClose(t *testing.T) {
	s := New()
	s.openQuery()
	s.openQuery()
	if len(s.tabs) != 3 || s.tabs[1].id == s.tabs[2].id {
		t.Fatal("tabs must have stable unique identity")
	}
	s.activeTab = 1
	s.closeActive()
	if len(s.tabs) != 2 || s.tabs[s.activeTab].id != 3 {
		t.Fatalf("tab closure should leave the next tab selected")
	}
	s.activeTab = 0
	s.closeActive()
	if len(s.tabs) != 2 {
		t.Fatal("Objects tab must not close")
	}
}

func TestWorkbenchMenuCommands(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建查询"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 2 || s.currentTab().kind != tabSQL {
		t.Fatalf("native menu did not open an SQL tab: %+v", s.tabs)
	}
	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("信息面板"); err != nil {
		t.Fatal(err)
	}
	if s.inspectorOn {
		t.Fatal("View menu should hide inspector")
	}
}

func TestWorkbenchTabCloseKeepsSelectionIdentity(t *testing.T) {
	s := New()
	s.openQuery() // id=2
	s.openQuery() // id=3, active
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("关闭 SQL 1"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 2 || s.tabs[s.activeTab].id != 3 {
		t.Fatalf("closing inactive tab changed active document: active=%d tabs=%+v", s.activeTab, s.tabs)
	}
	s.closeTab(0)
	if len(s.tabs) != 2 {
		t.Fatal("the permanent Objects tab should never close")
	}
	s.openQuery()
	s.closeOtherTabs(1)
	if len(s.tabs) != 2 || s.tabs[s.activeTab].id != 3 {
		t.Fatalf("closeOtherTabs did not preserve requested document: %+v", s.tabs)
	}
}

func TestWorkbenchToolbarNarrowWidth(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 840, 560)
	if !tt.HasText("连接") || !tt.HasText("对象") {
		t.Fatal("narrow workbench lost critical navigation")
	}
	if err := tt.Click("新建查询"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().kind != tabSQL {
		t.Fatal("narrow toolbar could not create SQL tab")
	}
	if err := tt.Click("信息"); err != nil {
		t.Fatal(err)
	}
	if s.inspectorOn {
		t.Fatal("info toggle must remain accessible at narrow width")
	}
}
