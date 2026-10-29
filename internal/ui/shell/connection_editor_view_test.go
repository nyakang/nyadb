package shell

import (
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
	if tt.HasText("密码仅在当前窗口内保留；安全保存将在密钥库接入后开放。") ||
		tt.HasText("支持严格证书验证的 TLS；未加密连接仅允许本机，请勿发送生产凭据。") {
		t.Fatal("native MySQL General tab must not show implementation notes")
	}
	if len(titles) != 1 || titles[0] != "新建连接 (MySQL)" {
		t.Fatalf("native title not updated: %v", titles)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("客户端驱动程序：") ||
		!tt.HasText("保持连接间隔（秒）：") || !tt.HasText("连接超时（秒）：") ||
		!tt.HasText("初始查询：") || !tt.HasText("兼容性") {
		t.Fatal("MySQL Advanced tab missing observed controls")
	}
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("使用自定义数据库列表") || !tt.HasText("自动打开") {
		t.Fatal("database tab should show the original custom database list")
	}
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("密码"); err != nil {
		t.Fatal(err)
	}
	tt.Type("session-only")
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 || s.connectionEditor.Active() ||
		s.ConnectionWindowTitle() != "新建连接" ||
		!tt.HasText("连接筛选") || titles[len(titles)-1] != "新建连接" {
		t.Fatal("Back did not restore the chooser, title and secret lifecycle")
	}
}
