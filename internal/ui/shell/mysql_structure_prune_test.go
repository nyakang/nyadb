package shell

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Same-named tables across databases are distinct workspaces, and losing one
// schema invalidates only its tabs while retaining the surviving tab cache.
func TestMySQLStructureQualifiedTabsAndPrunedSchema(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.objectWorkspace.Loader = func(_ context.Context, _ int64, database, table string) (*mysqlsvc.MySQLTableStructure, error) {
		return fakeStructure(database, table), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().title != "app_db.orders" {
		t.Fatalf("unqualified tab title: %q", s.currentTab().title)
	}

	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	if err := doubleClickDatabase(tt, "数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, 0)
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if len(s.tabs) != 3 || s.currentTab().title != "analytics.orders" {
		t.Fatalf("did not separate identically-named objects: %+v", s.tabs)
	}

	s.sessions.ReconcileDatabases([]string{"app_db"})
	tt.Frame()
	if len(s.tabs) != 2 || s.tabs[1].title != "app_db.orders" || s.activeTab != 0 {
		t.Fatalf("stale schema tab survived refresh: %+v active=%d", s.tabs, s.activeTab)
	}
	if s.tabs[1].object.Data() == nil {
		t.Fatal("pruning one schema incorrectly discarded valid structure")
	}
	if err := tt.Click("app_db.orders"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().object.Data() == nil || s.currentTab().object.Data().Database != "app_db" {
		t.Fatal("surviving tab could not be selected")
	}
}

func TestMySQLWorkbenchStatusReflectsConnectionLifecycle(t *testing.T) {
	s := New()
	if got := s.mysqlWorkbenchStatus(); got != "未连接" {
		t.Fatal(got)
	}
	for _, status := range []string{"连接中", "已连接", "连接失败"} {
		s.sessions.Current().Status = status
		if got := s.mysqlWorkbenchStatus(); got != status {
			t.Fatalf("displaying wrong session state: %q != %q", got, status)
		}
	}
}

func TestStructureSchemaPruneRevokesPendingReadAndPreservesSQLTab(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.objectWorkspace.Loader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		return fakeStructure(db, table), nil
	}
	s.openMySQLStructure(ui.Services{}, "app_db", "orders")
	waitMySQLStructure(t, s, tt)
	surviving := s.currentTab()
	s.openQuery()
	query := s.currentTab()
	query.query.Editor.Buffer.Set("SELECT preserved")
	started, canceled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.objectWorkspace.Loader = func(ctx context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		defer close(finished)
		return fakeStructure(db, "obsolete"), nil
	}
	s.openMySQLStructure(ui.Services{}, "analytics", "orders")
	removed := s.currentTab()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("pending schema read did not start")
	}
	s.activeTab = 2 // keep the SQL document selected during schema reconciliation
	s.sessions.ReconcileDatabases([]string{"app_db"})
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("schema pruning did not cancel its read")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("pruned worker did not return")
	}
	tt.Frame()
	if len(s.tabs) != 3 || s.currentTab() != query || query.query.Editor.Buffer.String() != "SELECT preserved" {
		t.Fatal("schema pruning changed active SQL document")
	}
	if removed.object.Loading() || removed.object.Data() != nil || surviving.object.Data() == nil || surviving.object.Data().Name != "orders" {
		t.Fatal("pruned read remained active or affected surviving metadata")
	}
	s.CloseMySQLSessions()
}
