package shell

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func waitTableChange(t *testing.T, s *State, tt interface{ Frame() }) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for s.mysqlTablePending && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.mysqlTablePending {
		t.Fatal("table operation did not settle")
	}
}

func TestMySQLTableMenuSharesActionsAndRequiresExplicitConfirmation(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var calls []connections.MySQLTableChange
	s.mysqlTableRunner = func(_ context.Context, id int64, r connections.MySQLTableChange) error {
		if id != s.mysqlSessionProfileID {
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
	if !s.mysqlTableDialog || s.mysqlTableAction != connections.MySQLTableDrop {
		t.Fatal("drop must open a confirmation, not execute DDL")
	}
	tt.Frame()
	if len(calls) != 0 {
		t.Fatal("DDL ran without confirmation")
	}
	s.mysqlTableConfirmation = "not_orders"
	tt.Frame()
	if err := tt.Click("确认删除表"); err == nil && len(calls) != 0 {
		t.Fatal("incorrect confirmation executed a drop")
	}
	s.mysqlTableConfirmation = "orders"
	tt.Frame()
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if len(calls) != 1 || calls[0].Action != connections.MySQLTableDrop ||
		calls[0].Table != "orders" || calls[0].Database != "app_db" {
		t.Fatalf("unexpected DDL: %+v", calls)
	}
}

func TestMySQLTableActionErrorIsVisibleAndDoesNotInvalidateOtherConnection(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlTableRunner = func(context.Context, int64, connections.MySQLTableChange) error {
		return errors.New("permission denied by server")
	}
	s.beginMySQLTableChange(s.mysqlSessionProfileID, "app_db", "orders", connections.MySQLTableRename)
	s.mysqlTableNewName = "orders_new"
	tt.Frame()
	if err := tt.Click("确认重命名"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if !s.mysqlTableDialog || s.mysqlTableError == "" ||
		!tt.HasText("permission denied by server") {
		t.Fatalf("error feedback missing: %q", s.mysqlTableError)
	}
}

func TestMySQLCreateTableUsesNameEnteredInDialog(t *testing.T) {
	s, tt := structureTestBrowser(t)
	requests := make(chan connections.MySQLTableChange, 1)
	s.mysqlTableRunner = func(_ context.Context, _ int64, request connections.MySQLTableChange) error {
		requests <- request
		return nil
	}
	if err := tt.RightClick("orders"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("新表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("new_orders")
	if err := tt.Click("确认新建表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	select {
	case request := <-requests:
		if request.Action != connections.MySQLTableCreate || request.Database != "app_db" || request.Table != "new_orders" {
			t.Fatalf("create must target the entered name, not the selected source table: %+v", request)
		}
	default:
		t.Fatal("confirmed creation did not reach the table service")
	}
	if s.status != "新建表成功：app_db.new_orders" {
		t.Fatalf("creation reported the wrong target: %q", s.status)
	}
}

func TestMySQLCreateTableIsReachableInEmptyDatabase(t *testing.T) {
	for _, entry := range []string{"database menu", "toolbar"} {
		t.Run(entry, func(t *testing.T) {
			s, tt := mysqlObjectTestState(t)
			s.mysqlObjectLister = func(context.Context, int64, string) ([]connections.MySQLDBObject, error) {
				return nil, nil
			}
			requests := make(chan connections.MySQLTableChange, 1)
			s.mysqlTableRunner = func(_ context.Context, _ int64, r connections.MySQLTableChange) error {
				requests <- r
				return nil
			}
			if err := tt.Click("数据库：app_db"); err != nil {
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
			if !s.mysqlTableDialog || s.mysqlTableAction != connections.MySQLTableCreate {
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
			case r := <-requests:
				if r.Action != connections.MySQLTableCreate || r.Database != "app_db" || r.Table != "first_table" {
					t.Fatalf("wrong empty-database creation request: %+v", r)
				}
			default:
				t.Fatal("creation was not submitted")
			}
		})
	}
}

func TestMySQLCopySubmenuDispatchesObservedActions(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlTableRunner = func(context.Context, int64, connections.MySQLTableChange) error { return nil }
	for _, entry := range []struct {
		label  string
		action connections.MySQLTableAction
	}{{"结构和数据", connections.MySQLTableCopyData}, {"仅结构", connections.MySQLTableCopyStructure}} {
		if err := tt.RightClick("orders"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("复制表", entry.label); err != nil {
			t.Fatal(err)
		}
		if !s.mysqlTableDialog || s.mysqlTableAction != entry.action {
			t.Fatalf("%s dispatched %s", entry.label, s.mysqlTableAction)
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
			calls := make(chan connections.MySQLTableChange, 1)
			s.mysqlTableRunner = func(_ context.Context, _ int64, r connections.MySQLTableChange) error {
				calls <- r
				return nil
			}
			for _, shortcut := range []struct {
				mods   ui.Modifiers
				key    ui.Key
				action connections.MySQLTableAction
			}{{0, ui.KeyF2, connections.MySQLTableRename}, {0, ui.KeyDelete, connections.MySQLTableDrop}, {ui.Ctrl, ui.KeyN, connections.MySQLTableCreate}} {
				if err := tt.Click(target); err != nil {
					t.Fatal(err)
				}
				tt.Key(shortcut.mods, shortcut.key)
				if !s.mysqlTableDialog || s.mysqlTableAction != shortcut.action || s.deleteConfirm {
					t.Fatalf("shortcut %v did not target the table action: dialog=%v action=%s profileDelete=%v", shortcut.key, s.mysqlTableDialog, s.mysqlTableAction, s.deleteConfirm)
				}
				input := "新表名称"
				if shortcut.action == connections.MySQLTableDrop {
					input = "确认原表名称"
				}
				if err := tt.Click(input); err != nil {
					t.Fatal(err)
				}
				tt.Key(0, ui.KeyDelete)
				tt.Key(ui.Ctrl, ui.KeyN)
				tt.Key(0, ui.KeyF2)
				if s.mysqlTableAction != shortcut.action || s.deleteConfirm {
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
			if s.mysqlTableDialog || s.deleteConfirm {
				t.Fatal("view shortcut dispatched a table or connection deletion")
			}
		})
	}
}

func TestMySQLObjectRefreshShortcutReloadsTreeAndGrid(t *testing.T) {
	for _, target := range []string{"orders", "InnoDB"} {
		t.Run(target, func(t *testing.T) {
			s, tt := structureTestBrowser(t)
			s.mysqlObjectLister = func(context.Context, int64, string) ([]connections.MySQLDBObject, error) {
				return []connections.MySQLDBObject{{Name: "orders", Kind: "表", Comment: "refreshed"}}, nil
			}
			if err := tt.Click(target); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyF5)
			waitMySQLObjects(t, s, tt)
			if objects := s.mysqlObjectCache["app_db"]; len(objects) != 1 || objects[0].Comment != "refreshed" {
				t.Fatalf("F5 did not refresh the current schema: %+v", objects)
			}
		})
	}
}
