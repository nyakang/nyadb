package shell

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestUnifiedNavigatorQualifiesObjectsAndLoadsOnlyOnDemand(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.sessions.Current().ProfileID
	calls := 0
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) { calls++; return nil, nil }
	for _, db := range []string{"app_db", "analytics"} {
		s.sessions.Current().Cache[db] = []mysqlsvc.MySQLDBObject{{Name: "same_table", Kind: "表", Engine: "InnoDB"}}
	}
	n := navigation.Node{Kind: "object", ProfileID: id, Database: "app_db", Category: "表", Object: "same_table"}
	other := n
	other.Database = "analytics"
	if n.Key() == other.Key() {
		t.Fatal("same-name tables share identity")
	}
	s.revealNavigationNode(n)
	tt.Frame()
	// A programmatic reveal and keyboard navigation must use the same qualified
	// node as row selection; rendering cached nodes does not schedule reads.
	s.selectNavigationNode(ui.Services{}, other.Key())
	s.revealNavigationNode(other)
	tt.Frame()
	if s.sessions.Current().Database != "analytics" || s.sessions.Current().Row != 0 || s.navigation.Key != other.Key() {
		t.Fatalf("selection lost its database: %s row=%d node=%+v", s.sessions.Current().Database, s.sessions.Current().Row, navigation.Parse(s.navigation.Key))
	}
	if calls != 0 {
		t.Fatal("cached tree traversal performed a query")
	}
	if err := tt.Click("全部折叠"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.Tree.Open.Len() != 0 {
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
	key := s.navigation.Key
	if key != navigation.ConnectionKey(p.ID) || s.sessions.Current().ProfileID != 0 {
		t.Fatal("selecting a profile connected or used its display name as identity")
	}
	p.Name = "After"
	if _, err = service.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if s.navigation.Key != key || !tt.HasText("After") {
		t.Fatal("rename lost selected profile identity")
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
			doc := &tab{kind: kind, title: "test"}
			if kind == tabStructure {
				s.nextTabID++
				doc.id = s.nextTabID
				doc.design = s.structureViewer.Open(doc.id, 0, "", "")
			}
			s.tabs = append(s.tabs, doc)
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
	s.sessions.ObjectLister = func(_ context.Context, _ int64, db string) ([]mysqlsvc.MySQLDBObject, error) {
		mu.Lock()
		reads[db]++
		mu.Unlock()
		return []mysqlsvc.MySQLDBObject{{Name: "table", Kind: "表"}}, nil
	}
	selected := s.navigation.Key
	for _, db := range s.sessions.Current().Databases {
		s.sessions.MarkOpened(s.sessions.Current().ProfileID, db)
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.sessions.Current().ProfileID, db).Key())
	}
	tt.Frame()
	waitMySQLObjects(t, s, tt)
	mu.Lock()
	defer mu.Unlock()
	if len(reads) != 2 || reads["app_db"] != 1 || reads["analytics"] != 1 {
		t.Fatalf("expansion cancelled a sibling request: %+v", reads)
	}
	if s.navigation.Key != selected || s.sessions.Current().Database != "" {
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
	if s.navigation.Group != "Folder" || s.selectedProfileID != 0 {
		t.Fatal("right-click target reverted to previously selected connection")
	}
}

func TestMetadataRefreshRetainsObjectIdentityWhenRowsMove(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.selectObjectInNavigator("app_db", "sales", "视图")
	s.sessions.Current().Row = 1
	tt.Frame()
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		return []mysqlsvc.MySQLDBObject{{Name: "new_first", Kind: "表"}, {Name: "orders", Kind: "表"}, {Name: "sales", Kind: "视图"}}, nil
	}
	if err := tt.Click("刷新元数据：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.sessions.Current().Row != 2 || navigation.Parse(s.navigation.Key).Object != "sales" {
		t.Fatalf("refresh retargeted selection: row=%d key=%+v", s.sessions.Current().Row, navigation.Parse(s.navigation.Key))
	}
}

func TestClickingInitiallySelectedObjectUpdatesInspector(t *testing.T) {
	s, tt := structureTestBrowser(t)
	if s.sessions.Current().Row != 0 {
		t.Fatal("expected initial object selection")
	}
	if err := tt.Click("orders"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	selected := navigation.Parse(s.navigation.Key)
	if selected.Kind != "object" || selected.Object != "orders" || selected.Database != "app_db" {
		t.Fatalf("first-row click left inspector on another scope: %+v", selected)
	}
}
