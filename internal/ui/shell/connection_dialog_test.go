package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionDialogChooseBackCancel(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDialog || s.connectionStep != 0 || !tt.HasText("选择一个连接类型：") {
		t.Fatal("connection action did not open chooser")
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 || s.connectionError != "" {
		t.Fatalf("disabled Next changed the chooser: step=%d error=%q open=%t", s.connectionStep, s.connectionError, s.connectionDialog)
	}
	if err := tt.Click("选择 PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionEditor.Draft().Kind != connections.PostgreSQL || s.connectionEditor.Draft().Port != 5432 ||
		s.connectionEditor.Draft().Database != "postgres" {
		t.Fatalf("unexpected PostgreSQL form: %+v", s.connectionEditor.Draft())
	}
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 {
		t.Fatal("back did not restore type chooser")
	}
	if err := tt.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog || s.connectionEditor.Active() {
		t.Fatal("cancel should close and clear session secret")
	}
}
