package shell

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestUnifiedNavigatorQualifiesObjectsAndLoadsOnlyOnDemand(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	calls := 0
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) { calls++; return nil, nil }
	for _, db := range []string{"app_db", "analytics"} {
		s.mysqlObjectCache[db] = []mysqlsvc.MySQLDBObject{{Name: "same_table", Kind: "表", Engine: "InnoDB"}}
	}
	n := navigationNode{Kind: "object", ProfileID: id, Database: "app_db", Category: "表", Object: "same_table"}
	other := n
	other.Database = "analytics"
	if n.key() == other.key() {
		t.Fatal("same-name tables share identity")
	}
	s.revealNavigationNode(n)
	tt.Frame()
	// A programmatic reveal and keyboard navigation must use the same qualified
	// node as row selection; rendering cached nodes does not schedule reads.
	s.selectNavigationNode(ui.Services{}, other.key())
	s.revealNavigationNode(other)
	tt.Frame()
	if s.mysqlSelectedDatabase != "analytics" || s.objectRow != 0 || s.explorerName != other.key() {
		t.Fatalf("selection lost its database: %s row=%d node=%+v", s.mysqlSelectedDatabase, s.objectRow, parseNavigationNode(s.explorerName))
	}
	if calls != 0 {
		t.Fatal("cached tree traversal performed a query")
	}
	if err := tt.Click("全部折叠"); err != nil {
		t.Fatal(err)
	}
	if s.explorerTree.Open.Len() != 0 {
		t.Fatal("collapse reopened an ancestor")
	}
}

func TestNavigatorUsesProfileIdentityAcrossRenameWithoutConnecting(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{Name: "Before", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.Click("Before"); err != nil {
		t.Fatal(err)
	}
	key := s.explorerName
	if key != profileNode(p.ID) || s.mysqlSessionProfileID != 0 {
		t.Fatal("selecting a profile connected or used its display name as identity")
	}
	p.Name = "After"
	if _, err = service.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if s.explorerName != key || !tt.HasText("After") {
		t.Fatal("rename lost selected profile identity")
	}
}

func TestUnifiedNavigatorVirtualizesMoreThanOneHundredObjects(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	items := make([]mysqlsvc.MySQLDBObject, 1500)
	for i := range items {
		items[i] = mysqlsvc.MySQLDBObject{Name: fmt.Sprintf("table_%04d", i), Kind: "表"}
	}
	s.mysqlObjectCache["app_db"] = items
	n := navigationNode{Kind: "category", ProfileID: s.mysqlSessionProfileID, Database: "app_db", Category: "表"}
	s.revealNavigationNode(n)
	s.explorerTree.Open.Add(n.key())
	tt.Frame()
	if s.explorerTree.Rows() < 1500 {
		t.Fatalf("tree truncated to %d nodes", s.explorerTree.Rows())
	}
	rendered := 0
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "table_") {
			rendered++
		}
	}
	if rendered >= 200 {
		t.Fatalf("tree eagerly rendered %d objects", rendered)
	}
}

func TestWorkbenchSplittersAreSharedAcrossDocumentKinds(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	s.sidebarPercent, s.centerPercent = 24, 71
	for _, kind := range []tabKind{tabSQL, tabStructure, tabMySQLData, tabObjects} {
		if kind == tabSQL {
			s.openQuery()
		} else if kind == tabObjects {
			s.activeTab = 0
		} else {
			s.tabs = append(s.tabs, &tab{kind: kind, title: "test"})
			s.activeTab = len(s.tabs) - 1
		}
		tt.Frame()
		if s.sidebarPercent != 24 || s.centerPercent != 71 {
			t.Fatalf("document %v reset pane widths", kind)
		}
	}
}

func TestEmptyObjectHeadersSurviveThemeScaleAndViewportChanges(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	for _, scale := range []float32{1, 1.25, 1.5} {
		tt.SetScale(scale)
		for _, size := range [][2]int{{1280, 800}, {840, 560}} {
			tt.SetSize(size[0], size[1])
			if !tt.HasText("名称") || !tt.HasText("自动递增值") {
				t.Fatal("empty object headers disappeared")
			}
			if tt.HasText("右键空白处新建连接") {
				t.Fatal("empty navigator advertises an unavailable action")
			}
			if err := tt.Click("连接"); err != nil {
				t.Fatal(err)
			}
			s.closeConnectionDialog()
			tt.Frame()
		}
	}
}

func TestExpandedDatabasesLoadSequentiallyWithoutChangingSelection(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	var mu sync.Mutex
	reads := map[string]int{}
	s.mysqlObjectLister = func(_ context.Context, _ int64, db string) ([]mysqlsvc.MySQLDBObject, error) {
		mu.Lock()
		reads[db]++
		mu.Unlock()
		return []mysqlsvc.MySQLDBObject{{Name: "table", Kind: "表"}}, nil
	}
	selected := s.explorerName
	for _, db := range s.mysqlSessionDatabases {
		s.markMySQLDatabaseOpened(s.mysqlSessionProfileID, db)
		s.explorerTree.Open.Add(databaseNode(s.mysqlSessionProfileID, db).key())
	}
	tt.Frame()
	waitMySQLObjects(t, s, tt)
	mu.Lock()
	defer mu.Unlock()
	if len(reads) != 2 || reads["app_db"] != 1 || reads["analytics"] != 1 {
		t.Fatalf("expansion cancelled a sibling request: %+v", reads)
	}
	if s.explorerName != selected || s.mysqlSelectedDatabase != "" {
		t.Fatal("expansion changed the selected object")
	}
}

func TestGroupContextTargetSurvivesNextFrame(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	if err := svc.CreateGroup(context.Background(), "Folder"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Create(context.Background(), connections.Profile{Name: "Connection", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.Click("Connection"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("Folder"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.CloseMenu()
	tt.Frame()
	if s.selectedGroup != "Folder" || s.selectedProfileID != 0 {
		t.Fatal("right-click target reverted to previously selected connection")
	}
}

func TestMetadataRefreshRetainsObjectIdentityWhenRowsMove(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.selectObjectInNavigator("app_db", "sales", "视图")
	s.objectRow = 1
	tt.Frame()
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		return []mysqlsvc.MySQLDBObject{{Name: "new_first", Kind: "表"}, {Name: "orders", Kind: "表"}, {Name: "sales", Kind: "视图"}}, nil
	}
	if err := tt.Click("刷新元数据：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.objectRow != 2 || parseNavigationNode(s.explorerName).Object != "sales" {
		t.Fatalf("refresh retargeted selection: row=%d key=%+v", s.objectRow, parseNavigationNode(s.explorerName))
	}
}

func TestClickingInitiallySelectedObjectUpdatesInspector(t *testing.T) {
	s, tt := structureTestBrowser(t)
	if s.objectRow != 0 {
		t.Fatal("expected initial object selection")
	}
	if err := tt.Click("orders"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	selected := parseNavigationNode(s.explorerName)
	if selected.Kind != "object" || selected.Object != "orders" || selected.Database != "app_db" {
		t.Fatalf("first-row click left inspector on another scope: %+v", selected)
	}
}
