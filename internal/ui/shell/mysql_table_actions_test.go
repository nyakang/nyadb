package shell

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func waitTableChange(t *testing.T, s *State, tt interface{ Frame() }) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for s.objectWorkspace.TablePending() && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.objectWorkspace.TablePending() {
		t.Fatal("table operation did not settle")
	}
}

func TestMySQLTableMenuSharesActionsAndRequiresExplicitConfirmation(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var calls []mysqlsvc.MySQLTableChange
	s.objectWorkspace.Runner = func(_ context.Context, id int64, r mysqlsvc.MySQLTableChange) error {
		if id != s.sessions.Current().ProfileID {
			return errors.New("wrong connection")
		}
		calls = append(calls, r)
		return nil
	}
	if err := tt.RightClick("orders"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	for _, action := range []string{"新建表", "删除表", "清空表", "截断表", "复制表", "重命名", "刷新"} {
		if !slices.Contains(menu, action) {
			t.Fatalf("tree menu missing %s: %v", action, menu)
		}
	}
	if err := tt.ChooseMenuItem("删除表"); err != nil {
		t.Fatal(err)
	}
	if !s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableAction() != mysqlsvc.MySQLTableDrop {
		t.Fatal("drop must open a confirmation, not execute DDL")
	}
	tt.Frame()
	if len(calls) != 0 {
		t.Fatal("DDL ran without confirmation")
	}
	setTestInput(t, tt, "确认原表名称", "not_orders")
	tt.Frame()
	if err := tt.Click("确认删除表"); err == nil && len(calls) != 0 {
		t.Fatal("incorrect confirmation executed a drop")
	}
	setTestInput(t, tt, "确认原表名称", "orders")
	tt.Frame()
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if len(calls) != 1 || calls[0].Action != mysqlsvc.MySQLTableDrop ||
		calls[0].Table != "orders" || calls[0].Database != "app_db" {
		t.Fatalf("unexpected DDL: %+v", calls)
	}
}

func TestMySQLCreateTableMenuDispatchesDialog(t *testing.T) {
	s, tt := structureTestBrowser(t)
	if err := tt.RightClick("orders"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建表"); err != nil {
		t.Fatal(err)
	}
	if !s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableAction() != mysqlsvc.MySQLTableCreate {
		t.Fatal("navigator creation menu did not open the object workspace dialog")
	}
}

func TestMySQLCreateTableIsReachableInEmptyDatabase(t *testing.T) {
	for _, entry := range []string{"database menu", "toolbar"} {
		t.Run(entry, func(t *testing.T) {
			s, tt := mysqlObjectTestState(t)
			s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
				return nil, nil
			}
			requests := make(chan mysqlsvc.MySQLTableChange, 1)
			s.objectWorkspace.Runner = func(_ context.Context, _ int64, r mysqlsvc.MySQLTableChange) error {
				requests <- r
				return nil
			}
			if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
				t.Fatal(err)
			}
			waitMySQLObjects(t, s, tt)
			if entry == "database menu" {
				if err := tt.RightClick("数据库：app_db"); err != nil {
					t.Fatal(err)
				}
				if err := tt.ChooseMenuItem("新建表"); err != nil {
					t.Fatal(err)
				}
			} else if err := tt.Click("新建表"); err != nil {
				t.Fatal(err)
			}
			if !s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableAction() != mysqlsvc.MySQLTableCreate {
				t.Fatal("empty database did not offer a creation dialog")
			}
			if err := tt.Click("新表名称"); err != nil {
				t.Fatal(err)
			}
			tt.Type("first_table")
			if err := tt.Click("确认新建表"); err != nil {
				t.Fatal(err)
			}
			waitTableChange(t, s, tt)
			select {
			case <-requests:
				t.Fatal("new table bypassed designer and preview confirmation")
			default:
			}
			current := s.currentTab()
			if current.object == nil || current.object.Database() != "app_db" || current.object.Table() != "first_table" || !current.object.DesignDirty() {
				t.Fatal("new table did not enter the editable designer")
			}
			tt.Frame()
			if e := tt.Click("SQL 预览"); e != nil {
				t.Fatal(e)
			}
			select {
			case <-requests:
				t.Fatal("preview executed DDL")
			default:
			}
		})
	}
}

func TestMySQLCopySubmenuDispatchesObservedActions(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.objectWorkspace.Runner = func(context.Context, int64, mysqlsvc.MySQLTableChange) error { return nil }
	for _, entry := range []struct {
		label  string
		action mysqlsvc.MySQLTableAction
	}{{"结构和数据", mysqlsvc.MySQLTableCopyData}, {"仅结构", mysqlsvc.MySQLTableCopyStructure}} {
		if err := tt.RightClick("orders"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("复制表", entry.label); err != nil {
			t.Fatal(err)
		}
		if !s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableAction() != entry.action {
			t.Fatalf("%s dispatched %s", entry.label, s.objectWorkspace.TableAction())
		}
		if err := tt.Click("取消"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMySQLTableShortcutsStayScopedAndRequireConfirmation(t *testing.T) {
	for _, target := range []string{"orders", "InnoDB"} { // navigator row and object-grid cell
		t.Run(target, func(t *testing.T) {
			s, tt := structureTestBrowser(t)
			calls := make(chan mysqlsvc.MySQLTableChange, 1)
			s.objectWorkspace.Runner = func(_ context.Context, _ int64, r mysqlsvc.MySQLTableChange) error {
				calls <- r
				return nil
			}
			for _, shortcut := range []struct {
				mods   ui.Modifiers
				key    ui.Key
				action mysqlsvc.MySQLTableAction
			}{{0, ui.KeyDelete, mysqlsvc.MySQLTableDrop}, {ui.Ctrl, ui.KeyN, mysqlsvc.MySQLTableCreate}} {
				if err := tt.Click(target); err != nil {
					t.Fatal(err)
				}
				tt.Key(shortcut.mods, shortcut.key)

				if !s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableAction() != shortcut.action || s.deleteConfirm {
					t.Fatalf("shortcut %v did not target the table action: dialog=%v action=%s profileDelete=%v", shortcut.key, s.objectWorkspace.TableDialogOpen(), s.objectWorkspace.TableAction(), s.deleteConfirm)
				}
				input := "新表名称"
				if shortcut.action == mysqlsvc.MySQLTableDrop {
					input = "确认原表名称"
				}
				if err := tt.Click(input); err != nil {
					t.Fatal(err)
				}
				tt.Key(0, ui.KeyDelete)
				tt.Key(ui.Ctrl, ui.KeyN)
				tt.Key(0, ui.KeyF2)
				if s.objectWorkspace.TableAction() != shortcut.action || s.deleteConfirm {
					t.Fatal("typing in dialog dispatched an object shortcut")
				}
				if err := tt.Click("取消"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case r := <-calls:
				t.Fatalf("shortcut ran SQL without confirmation: %+v", r)
			default:
			}
			if err := tt.Click("筛选对象：全部"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("sales"); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyDelete)
			tt.Key(0, ui.KeyF2)
			if s.objectWorkspace.TableDialogOpen() || s.deleteConfirm {
				t.Fatal("view shortcut dispatched a table or connection deletion")
			}
		})
	}
}

func TestMySQLObjectRefreshShortcutReloadsTreeAndGrid(t *testing.T) {
	for _, target := range []string{"orders", "InnoDB"} {
		t.Run(target, func(t *testing.T) {
			s, tt := structureTestBrowser(t)
			s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
				return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表", Comment: "refreshed"}}, nil
			}
			if err := tt.Click(target); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyF5)
			waitMySQLObjects(t, s, tt)
			if objects := s.sessions.Current().Cache["app_db"]; len(objects) != 1 || objects[0].Comment != "refreshed" {
				t.Fatalf("F5 did not refresh the current schema: %+v", objects)
			}
		})
	}
}

func TestMySQLObjectListF2RenamesInlineWithOriginalTarget(t *testing.T) {
	s, tt := structureTestBrowser(t)
	calls := make(chan mysqlsvc.MySQLTableChange, 1)
	s.objectWorkspace.Runner = func(_ context.Context, _ int64, r mysqlsvc.MySQLTableChange) error { calls <- r; return nil }
	if e := tt.Click("InnoDB"); e != nil {
		t.Fatal(e)
	}
	tt.Key(0, ui.KeyF2)
	if s.objectWorkspace.TableDialogOpen() || s.deleteConfirm {
		t.Fatal("list F2 must edit the selected row in place")
	}
	tt.Type("orders_cancelled")
	tt.Key(0, ui.KeyEscape)
	select {
	case r := <-calls:
		t.Fatalf("cancelled inline rename executed: %+v", r)
	default:
	}
	tt.Key(0, ui.KeyF2)
	tt.Type("orders_renamed")
	tt.Key(0, ui.KeyEnter)
	select {
	case r := <-calls:
		if r.Action != mysqlsvc.MySQLTableRename || r.Table != "orders" || r.NewName != "orders_renamed" || r.Database != "app_db" {
			t.Fatalf("inline rename lost original target: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("inline Enter did not execute rename")
	}
	waitTableChange(t, s, tt)
}
