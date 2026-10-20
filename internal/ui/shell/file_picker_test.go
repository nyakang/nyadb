package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLitePickerUsesInjectedNativeCommand(t *testing.T) {
	s := New()
	calls := 0
	s.SetSQLiteFilePicker(func() {
		calls++
		s.SelectSQLiteFile("/tmp/picked.sqlite")
	})
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("浏览文件"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || s.connectionDraft.Database != "/tmp/picked.sqlite" {
		t.Fatalf("file picker did not update the draft: calls=%d draft=%+v", calls, s.connectionDraft)
	}
	s.closeConnectionDialog()
	s.SelectSQLiteFile("/tmp/late.sqlite")
	if s.connectionDraft.Database != "/tmp/picked.sqlite" {
		t.Fatal("late native callback changed closed dialog")
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.SQLite)
	s.nextConnectionStep()
	s.SQLiteFilePickerError()
	if s.connectionError == "" {
		t.Fatal("native picker errors must be visible")
	}
}
