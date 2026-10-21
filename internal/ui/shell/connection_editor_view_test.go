package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLNativeEditorTabsAndBack(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	var titles []string
	s.SetConnectionWindowTitleUpdater(func(title string) {
		titles = append(titles, title)
	})
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if s.ConnectionWindowTitle() != "新建连接 (MySQL)" ||
		!tt.HasText("连接名称：") || !tt.HasText("主机：") ||
		!tt.HasText("端口：") || !tt.HasText("用户名：") ||
		!tt.HasText("密码：") || !tt.HasText("保存密码") {
		t.Fatal("native MySQL General tab is incomplete")
	}
	if len(titles) != 1 || titles[0] != "新建连接 (MySQL)" {
		t.Fatalf("native title not updated: %v", titles)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("高级 配置尚未实现") {
		t.Fatal("Advanced tab should remain navigable but clearly pending")
	}
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 2 || !tt.HasText("默认数据库：") {
		t.Fatal("database tab should expose the existing profile database field")
	}
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	s.connectionSecret = "session-only"
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 || s.connectionSecret != "" ||
		s.ConnectionWindowTitle() != "新建连接" ||
		!tt.HasText("连接筛选") || titles[len(titles)-1] != "新建连接" {
		t.Fatal("Back did not restore the chooser, title and secret lifecycle")
	}
}

func TestMySQLEditorValidationAndSecretNotInProfile(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.connectionError, "连接名称") {
		t.Fatalf("blank name must be rejected: %q", s.connectionError)
	}
	s.connectionDraft.Name = "localhost development"
	s.connectionSecret = "not persisted"
	if p, err := s.draftProfile(); err != nil || p.Name == "" {
		t.Fatalf("valid draft not accepted: %+v %v", p, err)
	}
	if err := tt.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if s.connectionSecret != "" || s.connectionDialog {
		t.Fatal("Cancel should clear password and close")
	}
}
