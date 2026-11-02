package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func shellCopyPreview(t *testing.T, s *State, tt *ui.Tester, destination string, data bool) {
	t.Helper()
	s.activeTab = 0
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: s.sessions.Current().ProfileID, Database: "main", Category: "表", Object: "t"})
	tt.Frame()
	if err := tt.RightClick("t"); err != nil {
		t.Fatal(err)
	}
	label := "仅结构"
	if data {
		label = "结构和数据"
	}
	if err := tt.ChooseMenuItem("复制表", label); err != nil {
		t.Fatal(err)
	}
	shellDropPreview(t, tt)
	waitTableChange(t, s, tt)
	setTestInput(t, tt, "目标表名称", destination)
}

func TestSQLiteCopyShellMenuPreviewTypedRowsAndSourceTabsRemain(t *testing.T) {
	for _, data := range []bool{false, true} {
		t.Run(map[bool]string{false: "structure", true: "data"}[data], func(t *testing.T) {
			s, tt, services, id := sqliteDropShell(t)
			s.openMySQLData(services, "main", "t")
			d := s.currentTab().data
			waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
			s.openMySQLStructure(services, "main", "t")
			o := s.currentTab().object
			waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
			shellCopyPreview(t, s, tt, "copied", data)
			session, _ := s.engineSession(id)
			target := s.runtime.Namespace(id, "main").Object("table", "copied")
			if _, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10}); err == nil {
				t.Fatal("preview created destination")
			}
			tt.SetSize(840, 560)
			tt.Frame()
			if err := tt.Click("确认复制表"); err != nil {
				t.Fatal(err)
			}
			waitTableChange(t, s, tt)
			if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
				t.Fatal("copy failed", s.objectWorkspace.TableError())
			}
			page, err := session.Page(context.Background(), contract.PageRequest{Object: target, Limit: 10})
			count := 0
			if data {
				count = 1
			}
			if err != nil || len(page.Rows) != count {
				t.Fatalf("copied rows: %+v %v", page, err)
			}
			original := target
			original.Name = "t"
			page, err = session.Page(context.Background(), contract.PageRequest{Object: original, Limit: 10})
			if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "preserved before confirmation" {
				t.Fatal("source changed", err)
			}
			keptData, keptObject := false, false
			for _, tab := range s.tabs {
				keptData = keptData || tab.data == d
				keptObject = keptObject || tab.object == o
			}
			if !keptData || !keptObject {
				t.Fatal("copy disposed original documents")
			}
			waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
			found := false
			for _, obj := range s.sessions.Current().Cache["main"] {
				if obj.Name == "copied" {
					found = true
				}
			}
			if !found {
				t.Fatal("destination missing from refreshed cache")
			}
		})
	}
}

func TestSQLiteCopyShellDirtyDesignDecisionAndRevisionConflict(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(map[bool]string{false: "discard", true: "save causes conflict"}[save], func(t *testing.T) {
			s, tt, services, id := sqliteDropShell(t)
			s.openMySQLStructure(services, "main", "t")
			d := s.currentTab().object
			waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
			s.objectWorkspace.BeginSchemaRename(d, "renamed_source", services)
			waitSQLiteDesigner(t, tt, func() bool { return !d.DesignBusy() && d.DesignDirty() })
			shellCopyPreview(t, s, tt, "copied", true)
			if err := tt.Click("确认复制表"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !tt.HasText("表设计有未保存的修改") || s.objectWorkspace.TablePending() {
				t.Fatal("copy bypassed original dirty-design guard")
			}
			choice := "放弃修改"
			if save {
				choice = "SQL 预览"
			}
			if save {
				for i := 0; i < 5 && !tt.Focused(choice); i++ {
					tt.Key(ui.Shift, ui.KeyTab)
				}
				if !tt.Focused(choice) {
					t.Fatal("cannot focus leave-decision SQL preview")
				}
				tt.Key(0, ui.KeyEnter)
			} else if err := tt.Click(choice); err != nil {
				t.Fatal(err)
			}
			if save {
				tt.Frame()
				tt.Frame()
				if err := tt.Click("确认执行结构 SQL"); err != nil {
					t.Fatal(err)
				}
			}
			waitSQLiteDesigner(t, tt, func() bool { return !d.DesignBusy() && !s.objectWorkspace.TablePending() })
			if save {
				if s.objectWorkspace.TableError() == "" || !s.objectWorkspace.TableDialogOpen() {
					t.Fatal("saved source revision allowed stale copy")
				}
				session, _ := s.engineSession(id)
				if _, err := session.Page(context.Background(), contract.PageRequest{Object: s.runtime.Namespace(id, "main").Object("table", "copied"), Limit: 10}); err == nil {
					t.Fatal("stale copy created destination")
				}
			} else if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
				t.Fatal("discarded draft copy failed", s.objectWorkspace.TableError())
			}
		})
	}
}

func TestSQLiteCopyShellDataDraftCanCancelAndThenSaveBeforeCopy(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Text, Text: "saved before copy"})
	if !d.HasDraft() {
		t.Fatal("cannot stage fixture draft")
	}
	shellCopyPreview(t, s, tt, "copied", true)
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.HasDraft() || s.objectWorkspace.TablePending() {
		t.Fatal("copy bypassed data draft")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.HasDraft() {
		t.Fatal("cancel discarded source draft")
	}
	if err := tt.Click("确认复制表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("应用更改"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteDesigner(t, tt, func() bool { return !d.HasDraft() && !s.objectWorkspace.TablePending() })
	if s.objectWorkspace.TableError() != "" || s.objectWorkspace.TableDialogOpen() {
		t.Fatal("saved rows copy failed", s.objectWorkspace.TableError())
	}
	session, _ := s.engineSession(id)
	page, err := session.Page(context.Background(), contract.PageRequest{Object: s.runtime.Namespace(id, "main").Object("table", "copied"), Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "saved before copy" {
		t.Fatal("copy ignored saved source rows", err)
	}
}

func TestSQLiteCopyShellForeignReceiptCannotRevokeReplacementOrLoseDraft(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Text, Text: "keep draft"})
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, "main").Object("table", "t"), Generation: s.runtime.Generation(id)}
	event := objectworkspace.SchemaCopyEvent{Original: target, Generation: s.sessions.Current().Generation, Database: "main", Destination: "copied", Outcome: contract.SchemaDesignOutcome{State: "uncertain"}}
	event.Original.Generation++
	s.schemaTableCopied(services, event)
	if !s.runtime.IsConnected(id) || !d.HasDraft() {
		t.Fatal("stale published receipt revoked replacement or lost draft")
	}
	event.Original = target
	s.schemaTableCopied(services, event)
	if s.runtime.IsConnected(id) || !d.HasDraft() || s.currentTab().data != d {
		t.Fatal("uncertain original receipt lost draft or failed to revoke original")
	}
}
