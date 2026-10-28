package shell

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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
