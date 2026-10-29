package shell

import (
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func objectInt64(n int64) *int64 { return &n }

func TestMySQLObjectMetadataColumnsRespectServerNullsAndEstimates(t *testing.T) {
	table := connections.MySQLDBObject{
		Name: "orders", Kind: "表", Engine: "InnoDB", Comment: "sales",
		AutoIncrement: objectInt64(71),
		ModifiedAt:    "2026-10-20 12:34:56",
		DataLength:    objectInt64(16384),
		RowsEstimate:  objectInt64(0),
	}
	expected := []string{"orders", "71", "2026-10-20 12:34:56", "16384", "InnoDB", "≈0", "sales"}
	for col, want := range expected {
		if got := mysqlObjectCellText(table, col); got != want {
			t.Fatalf("metadata column %d: want %q got %q", col, want, got)
		}
	}
	view := connections.MySQLDBObject{Name: "report_view", Kind: "视图", Comment: "report"}
	for _, col := range []int{1, 2, 3, 4, 5} {
		if got := mysqlObjectCellText(view, col); got != "" {
			t.Fatalf("unknown view metadata was invented: column %d %q", col, got)
		}
	}
}

func TestMySQLObjectFilterRetainsIdentityAndClearsHiddenSelection(t *testing.T) {
	s, tt := structureTestBrowser(t)
	id := s.mysqlSessionProfileID
	s.selectObjectInNavigator("app_db", "sales", "视图")
	s.objectRow = 1
	s.filterMySQLObjects("视图")
	tt.Frame()
	if s.objectRow != 0 || parseNavigationNode(s.explorerName).Object != "sales" {
		t.Fatalf("view lost qualified identity on filter: row=%d node=%+v", s.objectRow, parseNavigationNode(s.explorerName))
	}
	s.filterMySQLObjects("表")
	tt.Frame()
	node := parseNavigationNode(s.explorerName)
	if s.objectRow != -1 || node.Kind != "category" || node.Category != "表" ||
		node.Database != "app_db" || node.ProfileID != id {
		t.Fatalf("hidden view remained selected: row=%d node=%+v", s.objectRow, node)
	}
	s.filterMySQLObjects("全部")
	tt.Frame()
	node = parseNavigationNode(s.explorerName)
	if node.Kind != "database" || node.Database != "app_db" || s.objectRow != -1 {
		t.Fatalf("all filter retained stale selection: %+v, row=%d", node, s.objectRow)
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
		if s.mysqlObjectKind != "视图" || len(s.mysqlSelectedObjects()) != 1 {
			t.Fatalf("view filter did not apply at %dx%d: %q", size[0], size[1], s.mysqlObjectKind)
		}
		if err := tt.Click("筛选对象：全部"); err != nil {
			t.Fatalf("reset filter clipped at %dx%d: %v", size[0], size[1], err)
		}
		tt.Frame()
		if s.mysqlObjectKind != "全部" {
			t.Fatalf("all filter did not apply at %dx%d: %q", size[0], size[1], s.mysqlObjectKind)
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
