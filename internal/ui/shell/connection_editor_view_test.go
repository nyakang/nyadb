package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLNativeEditorTitleAndBack(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	var titles []string
	s.SetConnectionWindowTitleUpdater(func(title string) {
		titles = append(titles, title)
	})
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if s.ConnectionWindowTitle() != "新建连接 (MySQL)" {
		t.Fatal("native MySQL editor title is incorrect")
	}
	if len(titles) != 1 || titles[0] != "新建连接 (MySQL)" {
		t.Fatalf("native title not updated: %v", titles)
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
