package shell

import (
	"context"
	"errors"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func fakeStructure(database, table string) *connections.MySQLTableStructure {
	def := "1"
	return &connections.MySQLTableStructure{
		Database: database, Name: table, Kind: "BASE TABLE", Engine: "InnoDB",
		Columns: []connections.MySQLStructureColumn{
			{Name: "id", Type: "bigint", Position: 1, Key: "PRI", Default: &def},
		},
		Indexes: []connections.MySQLStructureIndex{{
			Name: "PRIMARY", Unique: true, Type: "BTREE",
			Columns: []connections.MySQLStructureIndexColumn{{Name: "id", Position: 1}},
		}},
		ForeignKeys: []connections.MySQLStructureForeignKey{{
			Name: "fk_order", ReferencedDatabase: database, ReferencedTable: "accounts",
			OnUpdate: "CASCADE", OnDelete: "RESTRICT",
			Columns: []connections.MySQLStructureForeignKeyColumn{{Name: "account_id", ReferencedName: "id", Position: 1}},
		}},
	}
}
func waitMySQLStructure(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.currentTab().kind == tabStructure && s.currentTab().structureLoading &&
		time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.currentTab().kind != tabStructure || s.currentTab().structureLoading {
		t.Fatal("structure request did not settle")
	}
}
func structureTestBrowser(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s, tt := mysqlObjectTestState(t)
	s.mysqlObjectLister = func(context.Context, int64, string) ([]connections.MySQLDBObject, error) {
		return []connections.MySQLDBObject{
			{Name: "orders", Kind: "表", Engine: "InnoDB"},
			{Name: "sales", Kind: "视图"},
		}, nil
	}
	if err := tt.Click("浏览数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	return s, tt
}

func TestMySQLStructureNativeTabsSwitchRefreshAndDisconnect(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var mu sync.Mutex
	calls := map[string]int{}
	s.mysqlStructureLoader = func(_ context.Context, _ int64, database, table string) (*connections.MySQLTableStructure, error) {
		mu.Lock()
		calls[table]++
		mu.Unlock()
		return fakeStructure(database, table), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().kind != tabStructure || s.currentTab().structureTable != "orders" ||
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
	s.objectRow = 1
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().structureTable != "sales" || len(s.tabs) != 3 {
		t.Fatalf("second table did not open in its own tab: %+v", s.tabs)
	}
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	s.objectRow = 0
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.tabs) != 3 || s.currentTab().structureTable != "orders" {
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

func TestMySQLStructureLateResultCannotReplaceRefreshOrDisconnectedSession(t *testing.T) {
	s, tt := structureTestBrowser(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	s.mysqlStructureLoader = func(ctx context.Context, id int64, database, table string) (*connections.MySQLTableStructure, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(started)
			<-release // deliberately ignores context, like a misbehaving driver
			return fakeStructure(database, "obsolete"), nil
		}
		return fakeStructure(database, table), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("load never started")
	}
	tab := s.currentTab()
	s.cancelMySQLStructure(tab)
	tt.Frame()
	if err := tt.Click("刷新表结构：orders"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	close(release)
	for i := 0; i < 4; i++ {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if tab.structure == nil || tab.structure.Name != "orders" {
		t.Fatalf("old result overwrote refreshed tab: %+v", tab.structure)
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 {
		t.Fatalf("disconnected metadata tab remained: %d", len(s.tabs))
	}
}

func TestMySQLStructureErrorRetryCloseAndCancel(t *testing.T) {
	s, tt := structureTestBrowser(t)
	fail := true
	s.mysqlStructureLoader = func(context.Context, int64, string, string) (*connections.MySQLTableStructure, error) {
		if fail {
			return nil, errors.New("metadata access denied")
		}
		return fakeStructure("app_db", "orders"), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().structureErr == "" || !strings.Contains(strings.Join(tt.Texts(), " "), "access denied") {
		t.Fatal("error state not shown")
	}
	fail = false
	if err := tt.Click("重试读取结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().structure == nil || s.currentTab().structureErr != "" {
		t.Fatal("retry failed to recover")
	}
	if err := tt.Click("关闭 app_db.orders"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 1 || s.currentTab().kind != tabObjects {
		t.Fatal("close did not restore objects")
	}
}
