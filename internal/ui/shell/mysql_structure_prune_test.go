package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Same-named tables across databases are distinct workspaces, and losing one
// schema invalidates only its tabs while retaining the surviving tab cache.
func TestMySQLStructureQualifiedTabsAndPrunedSchema(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlStructureLoader = func(_ context.Context, _ int64, database, table string) (*connections.MySQLTableStructure, error) {
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
	if err := tt.Click("数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	s.objectRow = 0
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if len(s.tabs) != 3 || s.currentTab().title != "analytics.orders" {
		t.Fatalf("did not separate identically-named objects: %+v", s.tabs)
	}

	s.reconcileMySQLDatabases([]string{"app_db"})
	tt.Frame()
	if len(s.tabs) != 2 || s.tabs[1].title != "app_db.orders" || s.activeTab != 0 {
		t.Fatalf("stale schema tab survived refresh: %+v active=%d", s.tabs, s.activeTab)
	}
	if s.tabs[1].structure == nil {
		t.Fatal("pruning one schema incorrectly discarded valid structure")
	}
	if err := tt.Click("app_db.orders"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().structure == nil || s.currentTab().structure.Database != "app_db" {
		t.Fatal("surviving tab could not be selected")
	}
}

func TestMySQLWorkbenchStatusReflectsConnectionLifecycle(t *testing.T) {
	s := New()
	if got := s.mysqlWorkbenchStatus(); got != "未连接" {
		t.Fatal(got)
	}
	for _, status := range []string{"连接中", "已连接", "连接失败"} {
		s.mysqlSessionStatus = status
		if got := s.mysqlWorkbenchStatus(); got != status {
			t.Fatalf("displaying wrong session state: %q != %q", got, status)
		}
	}
}
