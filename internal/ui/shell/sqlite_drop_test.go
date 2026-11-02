package shell

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func sqliteDropShell(t *testing.T) (*State, *ui.Tester, ui.Services, int64) {
	t.Helper()
	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "SQLite drop fixture", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "owned.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Status != "连接中" })
	if s.sessions.Current().Status != "已连接" {
		t.Fatal(s.sessions.Current().Error)
	}
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := session.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Execute(context.Background(), "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO t VALUES(1,'preserved before confirmation'); CREATE TABLE other(id INTEGER PRIMARY KEY)")
	l.Close()
	if err != nil {
		t.Fatal(err)
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, "main"))
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
	return s, tt, services, p.ID
}

func shellDropPreview(t *testing.T, tt *ui.Tester) {
	t.Helper()
	tt.Frame()
	if err := tt.Click("重新读取结构"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteDropShellKeyboardPreviewAndOriginalTabCleanup(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.openMySQLStructure(services, "main", "t")
	o := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
	s.openMySQLData(services, "main", "other")
	other := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !other.Loading() })
	// Return to the object list; Delete targets a table, never its profile.
	s.activeTab = 0
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: id, Database: "main", Category: "表", Object: "t"})
	tt.Frame()
	if err := tt.Click("t"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDelete)
	if !s.objectWorkspace.TableDialogOpen() || s.deleteConfirm {
		t.Fatalf("SQLite Delete failed to target the table: selected=%s availability=%v dialog=%v profileDelete=%v", s.navigation.Key, s.objectWorkspace.CanDropTable(id, "main", "t"), s.objectWorkspace.TableDialogOpen(), s.deleteConfirm)
	}
	shellDropPreview(t, tt)
	waitTableChange(t, s, tt)
	target := s.runtime.Namespace(id, "main").Object("table", "t")
	session, _ := s.engineSession(id)
	page, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("preview changed original: %+v %v", page, err)
	}
	tt.SetSize(840, 560)
	tt.Frame()
	setTestInput(t, tt, "确认原表名称", "wrong")
	if err := tt.Click("确认删除表"); err == nil && s.objectWorkspace.TablePending() {
		t.Fatal("incorrect name dispatched drop")
	}
	setTestInput(t, tt, "确认原表名称", "t")
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if s.objectWorkspace.TableError() != "" || s.objectWorkspace.TableDialogOpen() {
		t.Fatalf("drop failed: %s", s.objectWorkspace.TableError())
	}
	if _, err = session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("confirmed table remains")
	}
	for _, tab := range s.tabs {
		if tab.data == d || tab.object == o {
			t.Fatal("drop retained original data/structure tab")
		}
	}
	if s.findDocument(tabMySQLData, id, "main", "表", "other") == nil {
		t.Fatal("drop closed unrelated tab")
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	for _, obj := range s.sessions.Current().Cache["main"] {
		if obj.Name == "t" {
			t.Fatal("drop retained original tree/cache node")
		}
	}
}

func TestSQLiteDropShellRespectsDirtyDesignBeforeAcknowledgedDrop(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLStructure(services, "main", "t")
	d := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.objectWorkspace.BeginSchemaRename(d, "dirty_name", services)
	waitSQLiteDesigner(t, tt, func() bool { return !d.DesignBusy() && d.DesignDirty() })
	// Context-menu dispatch is shared by the navigator and object list.
	s.activeTab = 0
	tt.Frame()
	if err := tt.RightClick("t"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除表"); err != nil {
		t.Fatal(err)
	}
	shellDropPreview(t, tt)
	waitTableChange(t, s, tt)
	setTestInput(t, tt, "确认原表名称", "t")
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("表设计有未保存的修改") || s.objectWorkspace.TablePending() {
		t.Fatal("dirty design guard not visible before drop")
	}
	session, _ := s.engineSession(id)
	if _, err := session.Page(context.Background(), contract.PageRequest{Object: s.runtime.Namespace(id, "main").Object("table", "t"), Limit: 10}); err != nil {
		t.Fatal("guard allowed early drop", err)
	}
	if err := tt.Click("放弃修改"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
		t.Fatal("guarded drop failed", s.objectWorkspace.TableError())
	}
}

func TestSQLiteDropShellStalePublishedReceiptCannotCloseReplacementSession(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, "main").Object("table", "t"), Generation: s.runtime.Generation(id)}
	contextGeneration := s.sessions.Current().Generation
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	// A receipt for another published handle must not invalidate/cache-close
	// the live handle, even if its contextual UI generation is unchanged.
	target.Generation++
	s.schemaTableDropped(services, objectworkspace.SchemaDropEvent{Original: target, Generation: contextGeneration, Database: "main", Outcome: contract.SchemaDesignOutcome{State: "uncertain"}})
	if !s.runtime.IsConnected(id) || s.currentTab().data != d {
		t.Fatal("stale drop revoked live replacement")
	}
}
