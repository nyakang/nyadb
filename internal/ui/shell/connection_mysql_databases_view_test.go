package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDatabaseTabNativeEmptyState(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("使用自定义数据库列表") ||
		!tt.HasText("自动打开") ||
		!tt.HasText("添加数据库到列表") ||
		!tt.HasText("从列表中移除数据库") {
		t.Fatal("native Database tab missing observed controls")
	}
	if s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom database list must be off by default")
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList ||
		!tt.HasText("需要建立数据库连接后才能读取列表") {
		t.Fatal("custom list toggle or offline explanation missing")
	}
	// The tester may return nil for a click on a disabled control; verify that
	// nothing was added, no network workflow opened and no state changed.
	_ = tt.Click("添加数据库到列表（需要连接 MySQL）")
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList || s.connectionError != "" {
		t.Fatal("disabled Add must not launch a database operation")
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom list toggle did not return to disabled")
	}
}

func TestMySQLDatabaseTabCustomFlagPersists(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "custom database list"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("save failed: %s", s.connectionError)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || !list[0].MySQLAdvanced.UseCustomDatabaseList {
		t.Fatalf("custom database-list flag was not persisted: %+v %v", list, err)
	}
	s.startEditProfile()
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom list flag missing from edit draft")
	}
	edited := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := edited.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if !edited.HasText("需要建立数据库连接后才能读取列表") {
		t.Fatal("custom list UI not restored on edit")
	}
}
