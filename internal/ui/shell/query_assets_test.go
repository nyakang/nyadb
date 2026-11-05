package shell

import (
	"bytes"
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type assetTestKey struct{ key []byte }

func (k *assetTestKey) Get() ([]byte, bool, error) { return bytes.Clone(k.key), len(k.key) > 0, nil }
func (k *assetTestKey) Set(v []byte) error         { k.key = bytes.Clone(v); return nil }
func TestQueryToolbarOfflineOpenUsesOriginalScopeAndReusesTab(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	var reads atomic.Int32
	s.sessions.DatabaseLister = func(context.Context, int64) ([]string, error) { reads.Add(1); return nil, nil }
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) { reads.Add(1); return nil, nil }
	ctx := context.Background()
	a, e := profiles.Create(ctx, connections.Profile{Name: "Query A", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "a.sqlite")})
	if e != nil {
		t.Fatal(e)
	}
	b, e := profiles.Create(ctx, connections.Profile{Name: "Query B", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "b.sqlite")})
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(b.Name)
	db, e := localdb.Open(filepath.Join(t.TempDir(), "queries.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store := queries.NewStore(localdb.NewSQLRepository(db), &assetTestKey{})
	n := queries.Named{ID: "q", ProfileID: a.ID, Name: "Original query", Database: "main", Text: "SELECT 1", Revision: 1}
	if e = store.SaveNamed(ctx, n); e != nil {
		t.Fatal(e)
	}
	s.SetSQLPersistence(store, sqlworkspace.Files{}, nil)
	deadline := time.Now().Add(time.Second)
	for s.sqlWorkspace.PersistenceLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if e = tt.Click("查询"); e != nil {
		t.Fatal(e)
	}
	if s.objectWorkspace.FilterKind(b.ID) != "查询" || !tt.HasText("命名查询") {
		t.Fatal("offline query toolbar unavailable")
	}
	s.selectProfileByName(a.Name)
	tt.Frame()
	if e = tt.Click("查询"); e != nil {
		t.Fatal(e)
	}
	if e = tt.Click(n.Name); e != nil {
		t.Fatal(e)
	}
	tt.Key(0, ui.KeyF2)
	if !tt.HasText("重命名查询") {
		t.Fatal("workbench F2 did not reach the query manager")
	}
	if e = tt.Click("取消"); e != nil {
		t.Fatal(e)
	}
	if children := s.navigation.Children(navigation.ConnectionKey(a.ID)); children != nil {
		t.Fatalf("offline connection still has disclosure children: %v", children)
	}
	tt.Frame()
	if e = tt.RightClick(n.Name); e != nil {
		t.Fatal(e)
	}
	s.selectProfileByName(b.Name)
	tt.Frame()
	if e = tt.ChooseMenuItem("打开查询"); e != nil {
		t.Fatal(e)
	}
	if s.selectedProfileID != b.ID || len(s.tabs) != 1 {
		t.Fatal("stale query browser menu activated its original profile")
	}
	s.openNamedQuery(n)
	tt.Frame()
	doc := s.currentTab().query
	if doc == nil || doc.RecoveryProfile() != a.ID || doc.RecoverySchema() != "main" || !doc.NeedsRecovery() || doc.Editor.Buffer.String() != n.Text {
		t.Fatal("query used current profile or skipped explicit recovery")
	}
	count := len(s.tabs)
	s.openNamedQuery(n)
	if len(s.tabs) != count || s.currentTab().query != doc {
		t.Fatal("opening named query duplicated its tab")
	}
	if reads.Load() != 0 || s.sessions.StatusFor(a.ID) == "已连接" || s.sessions.StatusFor(b.ID) == "已连接" {
		t.Fatal("offline query browsing connected or read database metadata")
	}
}
func TestQueryNavigatorNeverDispatchesDatabaseObjectCommands(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	p, e := profiles.Create(context.Background(), connections.Profile{Name: "Offline", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	key := (navigation.Node{Kind: "category", ProfileID: p.ID, Category: "查询"}).Key()
	s.selectNavigationNode(ui.Services{}, key)
	if s.sessions.Current().Database != "" || s.sessions.Current().Pending != "" || s.objectWorkspace.Category(p.ID) != "查询" {
		t.Fatal("query category mutated database state")
	}
	tt.Frame()
	if !tt.HasText("命名查询") {
		t.Fatal("query browser not hosted")
	}
	s.navigationShortcut(ui.Services{}, navigation.Node{Kind: "object", ProfileID: p.ID, Category: "查询", Object: "q"}, navigation.Shortcut{Modifiers: ui.Ctrl, Key: ui.KeyD})
	if s.currentTab().object != nil {
		t.Fatal("query shortcut opened structure document")
	}
}
func TestToolbarFunctionsAndOtherMenuKeepOriginalScope(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	p, e := profiles.Create(context.Background(), connections.Profile{Name: "MySQL", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Database = "app"
	s.sessions.MarkOpened(p.ID, "app")
	publishRuntimeFixture(t, s)
	tt := ui.NewTester(s.View, 1280, 800)
	if e = tt.Click("函数"); e != nil {
		t.Fatal(e)
	}
	if s.objectWorkspace.Category(p.ID) != "函数" {
		t.Fatal("function toolbar was not connected")
	}
	if e = tt.Click("其它"); e != nil {
		t.Fatal(e)
	}
	s.sessions.Current().Generation++
	tt.Frame()
	if e = tt.ChooseMenuItem("事件"); e != nil {
		t.Fatal(e)
	}
	if s.objectWorkspace.Category(p.ID) != "函数" {
		t.Fatal("stale toolbar menu switched category")
	}
	if e = tt.Click("其它"); e != nil {
		t.Fatal(e)
	}
	if e = tt.ChooseMenuItem("存储过程"); e != nil {
		t.Fatal(e)
	}
	if s.objectWorkspace.Category(p.ID) != "存储过程" {
		t.Fatal("other menu did not dispatch")
	}
	narrow := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.toolBar(c) }, 420, 80)
	if e = narrow.Click("更多"); e != nil {
		t.Fatal(e)
	}
	if e = narrow.ChooseMenuItem("查询"); e != nil {
		t.Fatal(e)
	}
	if s.objectWorkspace.Category(p.ID) != "查询" {
		t.Fatal("overflow query entry was not connected")
	}
}

type toolbarAdmin struct {
	mysqlsvc.Administration
	read chan int64
}

func (a *toolbarAdmin) SessionGeneration(int64) uint64 { return 17 }
func (a *toolbarAdmin) Accounts(ctx context.Context, id int64) ([]mysqlsvc.MySQLAccount, error) {
	if mysqlsvc.ExpectedMySQLSessionGeneration(ctx) != 17 {
		return nil, contract.ErrRevoked
	}
	a.read <- id
	return nil, nil
}

type toolbarAdminSession struct {
	*runtimeFixtureSession
	admin *toolbarAdmin
}

func (s *toolbarAdminSession) Administration() mysqlsvc.Administration { return s.admin }
func TestToolbarAccountsAndBackupUseInjectedOriginalAdministration(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	ctx := context.Background()
	p, e := profiles.Create(ctx, connections.Profile{Name: "Admin", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	admin := &toolbarAdmin{read: make(chan int64, 1)}
	s.runtime.Register(connections.MySQL, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		return &toolbarAdminSession{&runtimeFixtureSession{owner: s, id: p.ID}, admin}, nil
	}))
	if e = s.runtime.Connect(ctx, p, ""); e != nil {
		t.Fatal(e)
	}
	w := s.sessions.Current()
	w.ProfileID, w.Status, w.Database = p.ID, "已连接", "app"
	w.Generation = 31
	s.sessions.MarkOpened(p.ID, "app")
	s.SetBackupFiles(objectworkspace.BackupFiles{})
	tt := ui.NewTester(s.View, 1280, 800)
	if e = tt.Click("用户"); e != nil {
		t.Fatal(e)
	}
	select {
	case id := <-admin.read:
		if id != p.ID {
			t.Fatal("wrong administration profile")
		}
	case <-time.After(time.Second):
		t.Fatal("accounts not dispatched")
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !tt.HasText("MySQL 用户、角色与权限") {
		t.Fatal("account panel absent")
	}
	if e = tt.Click("关闭"); e != nil {
		t.Fatal(e)
	}
	if e = tt.Click("备份"); e != nil {
		t.Fatal(e)
	}
	if e = tt.ChooseMenuItem("备份数据库…"); e != nil {
		t.Fatal(e)
	}
	if !tt.HasText("连接 #1 · 数据库 app") || !tt.HasText("备份与 SQL 转储") {
		t.Fatalf("backup original scope absent: %v", tt.Texts())
	}
	if e = tt.Click("关闭"); e != nil {
		t.Fatal(e)
	}
	narrow := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.toolBar(c) }, 420, 80)
	if e = narrow.Click("更多"); e != nil {
		t.Fatal(e)
	}
	if e = narrow.ChooseMenuItem("备份"); e != nil {
		t.Fatal(e)
	}
	if e = narrow.ChooseMenuItem("导出结构 SQL…"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !tt.HasText("连接 #1 · 数据库 app") || !tt.HasText("备份与 SQL 转储") {
		t.Fatal("overflow backup submenu did not preserve the original target")
	}
}
