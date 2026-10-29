package shell

import (
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestMySQLObjectFilterRetainsIdentityAndClearsHiddenSelection(t *testing.T) {
	s, tt := structureTestBrowser(t)
	id := s.sessions.Current().ProfileID
	s.selectObjectInNavigator("app_db", "sales", "视图")
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, 1)
	s.filterMySQLObjects("视图")
	tt.Frame()
	if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) != 0 || navigation.Parse(s.navigation.Key).Object != "sales" {
		t.Fatalf("view lost qualified identity on filter: row=%d node=%+v", s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID), navigation.Parse(s.navigation.Key))
	}
	s.filterMySQLObjects("表")
	tt.Frame()
	node := navigation.Parse(s.navigation.Key)
	if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) != -1 || node.Kind != "category" || node.Category != "表" ||
		node.Database != "app_db" || node.ProfileID != id {
		t.Fatalf("hidden view remained selected: row=%d node=%+v", s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID), node)
	}
	s.filterMySQLObjects("全部")
	tt.Frame()
	node = navigation.Parse(s.navigation.Key)
	if node.Kind != "database" || node.Database != "app_db" || s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) != -1 {
		t.Fatalf("all filter retained stale selection: %+v, row=%d", node, s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID))
	}
}

func TestMySQLDatabaseMenuDoesNotOfferObjectActions(t *testing.T) {
	_, tt := structureTestBrowser(t)
	if err := tt.RightClick("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	if !slices.Contains(menu, "刷新") || slices.Contains(menu, "打开表") ||
		slices.Contains(menu, "设计表（只读）") || slices.Contains(menu, "显示全部对象") {
		t.Fatalf("database menu has wrong scope: %v", menu)
	}
	tt.CloseMenu()
}

func TestMySQLObjectFilterAvailableAtCompactViewportSizes(t *testing.T) {
	s, tt := structureTestBrowser(t)
	for _, size := range [][2]int{{1280, 800}, {840, 560}} {
		tt.SetSize(size[0], size[1])
		if err := tt.Click("筛选对象：视图"); err != nil {
			t.Fatalf("view filter clipped at %dx%d: %v", size[0], size[1], err)
		}
		tt.Frame()
		if s.objectWorkspace.FilterKind(s.sessions.Current().ProfileID) != "视图" || len(s.objectWorkspace.SelectedObjects()) != 1 {
			t.Fatalf("view filter did not apply at %dx%d: %q", size[0], size[1], s.objectWorkspace.FilterKind(s.sessions.Current().ProfileID))
		}
		if err := tt.Click("筛选对象：全部"); err != nil {
			t.Fatalf("reset filter clipped at %dx%d: %v", size[0], size[1], err)
		}
		tt.Frame()
		if s.objectWorkspace.FilterKind(s.sessions.Current().ProfileID) != "全部" {
			t.Fatalf("all filter did not apply at %dx%d: %q", size[0], size[1], s.objectWorkspace.FilterKind(s.sessions.Current().ProfileID))
		}
	}
}

func TestMySQLTableAndViewContextMenusUseCorrectObjectLabels(t *testing.T) {
	_, tt := structureTestBrowser(t)
	if err := tt.RightClick("sales"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	if !slices.Contains(menu, "打开视图（只读）") ||
		!slices.Contains(menu, "查看视图结构（只读）") ||
		slices.Contains(menu, "设计表（只读）") {
		t.Fatalf("view context menu offered table actions: %v", menu)
	}
	tt.CloseMenu()
	if err := tt.RightClick("orders"); err != nil {
		t.Fatal(err)
	}
	menu = tt.Menu()
	if !slices.Contains(menu, "打开表") || slices.Contains(menu, "打开表（只读）") ||
		!slices.Contains(menu, "设计表（只读）") ||
		slices.Contains(menu, "查看视图结构（只读）") {
		t.Fatalf("table context menu offered view actions: %v", menu)
	}
	tt.CloseMenu()
}
