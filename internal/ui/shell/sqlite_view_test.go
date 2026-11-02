package shell

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"testing"
)

func waitSQLiteView(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	waitSQLiteDesigner(t, tt, func() bool { return !s.objectWorkspace.ViewPending() })
	tt.Frame()
}
func shellCreateView(t *testing.T, s *State, tt *ui.Tester, name, sql string) {
	t.Helper()
	s.activeTab = 0
	tt.Frame()
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建对象", "新建视图"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteView(t, s, tt)
	setTestInput(t, tt, "视图名称", name)
	setTestInput(t, tt, "完整 CREATE VIEW 定义", sql)
	if err := tt.Click("重新读取原定义"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteView(t, s, tt)
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
}
func shellViewMenu(t *testing.T, s *State, tt *ui.Tester, id int64, name, label string) {
	t.Helper()
	s.activeTab = 0
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: id, Database: "main", Category: "视图", Object: name})
	tt.Frame()
	if err := tt.RightClick(name); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(label); err != nil {
		t.Fatal(err)
	}
	waitSQLiteView(t, s, tt)
}
func shellConfirmView(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("确认执行视图 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteView(t, s, tt)
	if s.objectWorkspace.ViewError() != "" || s.objectWorkspace.ViewOpen() {
		t.Fatal("native view execution failed", s.objectWorkspace.ViewError())
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
}

func TestSQLiteViewShellNativeMenusPreviewAndQualifiedTabLifecycle(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	session, _ := s.engineSession(id)
	shellCreateView(t, s, tt, "created_view", `CREATE VIEW "main"."created_view"(id,value) AS SELECT id,value FROM t`)
	target := s.runtime.Namespace(id, "main").Object("view", "created_view")
	if _, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("view preview executed SQL")
	}
	tt.SetSize(840, 560)
	tt.Frame()
	shellConfirmView(t, s, tt)
	s.openMySQLData(services, "main", "created_view")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.openMySQLStructure(services, "main", "created_view")
	o := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
	s.openMySQLData(services, "main", "other")
	other := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !other.Loading() })
	tt.SetSize(1280, 800)
	shellViewMenu(t, s, tt, id, "created_view", "编辑完整定义…")
	setTestInput(t, tt, "完整 CREATE VIEW 定义", `CREATE VIEW "main"."created_view"(id,value) AS SELECT id,upper(value) FROM t`)
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	before, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(before.Rows) != 1 || before.Rows[0][1].Text != "preserved before confirmation" {
		t.Fatal("alter preview changed saved view", err)
	}
	shellConfirmView(t, s, tt)
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() && !o.Loading() })
	page, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "PRESERVED BEFORE CONFIRMATION" {
		t.Fatal("alter did not update original view", err)
	}
	if d.Page() == nil || len(d.Page().Rows) != 1 || d.Page().Rows[0][1].Text != "PRESERVED BEFORE CONFIRMATION" {
		t.Fatal("alter did not refresh the native data document")
	}
	shellViewMenu(t, s, tt, id, "created_view", "重命名视图…")
	setTestInput(t, tt, "视图名称", "renamed_view")
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	shellConfirmView(t, s, tt)
	for _, tab := range s.tabs {
		if tab.data == d || tab.object == o {
			t.Fatal("rename retained old qualified view tabs")
		}
	}
	foundOther := false
	for _, tab := range s.tabs {
		foundOther = foundOther || tab.data == other
	}
	if !foundOther {
		t.Fatal("rename closed unrelated table")
	}
	target.Name = "renamed_view"
	if _, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10}); err != nil {
		t.Fatal("renamed view unavailable", err)
	}
	s.openMySQLData(services, "main", "renamed_view")
	renamed := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !renamed.Loading() })
	s.openQuery()
	q := s.currentTab().query
	q.Editor.Buffer.Set("SELECT unsaved_text")
	shellViewMenu(t, s, tt, id, "renamed_view", "删除对象…")
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	setTestInput(t, tt, "确认原视图名称", "wrong")
	if err := tt.Click("确认执行视图 SQL"); err == nil && s.objectWorkspace.ViewPending() {
		t.Fatal("wrong name dispatched deletion")
	}
	setTestInput(t, tt, "确认原视图名称", "renamed_view")
	shellConfirmView(t, s, tt)
	if _, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("dropped view remains")
	}
	for _, tab := range s.tabs {
		if tab.data == renamed {
			t.Fatal("drop retained removed view data")
		}
	}
	if q.Editor.Buffer.String() != "SELECT unsaved_text" {
		t.Fatal("view drop changed SQL text")
	}
	target.Kind, target.Name = "table", "t"
	page, err = session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "preserved before confirmation" {
		t.Fatal("view CRUD changed source rows", err)
	}
}

func TestSQLiteViewShellPreservesUnderlyingTableDraftAndRejectsBrokenDefinition(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Text, Text: "unsaved source draft"})
	if !d.HasDraft() {
		t.Fatal("fixture cannot stage draft")
	}
	shellCreateView(t, s, tt, "v", `CREATE VIEW v AS SELECT id,value FROM t`)
	shellConfirmView(t, s, tt)
	if !d.HasDraft() {
		t.Fatal("creating view saved/discarded underlying table draft")
	}
	shellViewMenu(t, s, tt, id, "v", "编辑完整定义…")
	setTestInput(t, tt, "完整 CREATE VIEW 定义", `CREATE VIEW v AS SELECT * FROM missing_source`)
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认执行视图 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteView(t, s, tt)
	if s.objectWorkspace.ViewError() == "" || !s.objectWorkspace.ViewOpen() || !d.HasDraft() {
		t.Fatal("failed view edit lost original state")
	}
	session, _ := s.engineSession(id)
	page, err := session.Page(context.Background(), contract.PageRequest{Object: s.runtime.Namespace(id, "main").Object("view", "v"), Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "preserved before confirmation" {
		t.Fatal("failed edit changed saved view", err)
	}
}

func TestSQLiteViewShellOldPublishedReceiptCannotRevokeReplacement(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openQuery()
	q := s.currentTab().query
	q.Editor.Buffer.Set("SELECT preserved")
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, "main").Object("view", "v"), Generation: s.runtime.Generation(id)}
	event := objectworkspace.ViewEvent{Request: contract.ViewDesignRequest{Original: contract.ViewDesignSnapshot{Target: target}, Action: "drop"}, Generation: s.sessions.Current().Generation, Database: "main", Outcome: contract.ViewDesignOutcome{State: "uncertain"}}
	event.Request.Original.Target.Generation++
	s.nativeViewChanged(services, event)
	tt.Frame()
	if !s.runtime.IsConnected(id) || q.Editor.Buffer.String() != "SELECT preserved" {
		t.Fatal("old view receipt revoked replacement or changed SQL")
	}
	event.Request.Original.Target = target
	s.nativeViewChanged(services, event)
	if s.runtime.IsConnected(id) || q.Editor.Buffer.String() != "SELECT preserved" {
		t.Fatal("uncertain original receipt failed to revoke or lost SQL")
	}
}
