package shell

import (
	"context"
	"image/png"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func fakeStructure(database, table string) *mysqlsvc.MySQLTableStructure {
	def := "1"
	return &mysqlsvc.MySQLTableStructure{
		Database: database, Name: table, Kind: "BASE TABLE", Engine: "InnoDB",
		Columns: []mysqlsvc.MySQLStructureColumn{
			{Name: "id", Type: "bigint", Position: 1, Key: "PRI", Default: &def},
		},
		Indexes: []mysqlsvc.MySQLStructureIndex{{
			Name: "PRIMARY", Unique: true, Type: "BTREE",
			Columns: []mysqlsvc.MySQLStructureIndexColumn{{Name: "id", Position: 1}},
		}},
		ForeignKeys: []mysqlsvc.MySQLStructureForeignKey{{
			Name: "fk_order", ReferencedDatabase: database, ReferencedTable: "accounts",
			OnUpdate: "CASCADE", OnDelete: "RESTRICT",
			Columns: []mysqlsvc.MySQLStructureForeignKeyColumn{{Name: "account_id", ReferencedName: "id", Position: 1}},
		}},
	}
}
func waitMySQLStructure(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.currentTab().kind == tabStructure && s.currentTab().object.Loading() &&
		time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.currentTab().kind != tabStructure || s.currentTab().object.Loading() {
		t.Fatalf("structure request did not settle: kind=%v loading=%v database=%q row=%d node=%+v", s.currentTab().kind, s.currentTab().object.Loading(), s.sessions.Current().Database, s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID), navigation.Parse(s.navigation.Key))
	}
}
func structureTestBrowser(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s, tt := mysqlObjectTestState(t)
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		return []mysqlsvc.MySQLDBObject{
			{Name: "orders", Kind: "表", Engine: "InnoDB"},
			{Name: "sales", Kind: "视图"},
		}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	return s, tt
}

func TestMySQLStructureNativeTabsSwitchRefreshAndDisconnect(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var mu sync.Mutex
	calls := map[string]int{}
	s.objectWorkspace.Loader = func(_ context.Context, _ int64, database, table string) (*mysqlsvc.MySQLTableStructure, error) {
		mu.Lock()
		calls[table]++
		mu.Unlock()
		return fakeStructure(database, table), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().kind != tabStructure || s.currentTab().object.Table() != "orders" ||
		!tt.HasText("id") || !tt.HasText("只读结构") {
		t.Fatalf("did not show structure: %v", tt.Texts())
	}
	if capture := os.Getenv("NYADB_MYSQL_STRUCTURE_CAPTURE"); capture != "" {
		file, err := os.Create(capture)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("索引"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("此表没有可见的次级索引") {
		t.Fatalf("primary key should remain in field attributes, not the secondary index list: %v", tt.Texts())
	}
	if err := tt.Click("外键"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("fk_order") {
		t.Fatalf("foreign keys not visible: %v", tt.Texts())
	}
	if err := tt.Click("表信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("存储引擎") {
		t.Fatalf("base info not visible: %v", tt.Texts())
	}
	if err := tt.Click("刷新表结构：orders"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	mu.Lock()
	count := calls["orders"]
	mu.Unlock()
	if count != 2 {
		t.Fatalf("refresh must reload: %d", count)
	}

	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, 1)
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().object.Table() != "sales" || len(s.tabs) != 3 {
		t.Fatalf("second table did not open in its own tab: %+v", s.tabs)
	}
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, 0)
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.tabs) != 3 || s.currentTab().object.Table() != "orders" {
		t.Fatal("reopening structure must focus the existing tab")
	}
	mu.Lock()
	count = calls["orders"]
	mu.Unlock()
	if count != 2 {
		t.Fatal("reopening loaded tab should use its cached structure")
	}
	if err := tt.Click("关闭 app_db.orders"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 2 {
		t.Fatalf("closing one tab removed unrelated tabs: %d", len(s.tabs))
	}
	s.disconnectMySQLSession()
	tt.Frame()
	for _, page := range s.tabs {
		if page.kind == tabStructure {
			t.Fatal("disconnect retained a table structure cache or tab")
		}
	}
}
