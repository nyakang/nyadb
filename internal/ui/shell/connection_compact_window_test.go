package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeConnectionChooserMinimumSizeAndSelection(t *testing.T) {
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	tt := ui.NewTester(s.ConnectionWindowView, 720, 520)
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 {
		t.Fatal("an unselected chooser must not advance")
	}
	for _, label := range []string{"选择 MySQL", "选择 PostgreSQL", "选择 SQLite", "取消连接向导", "下一步"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 520 {
			t.Fatalf("minimum chooser clips %q: %+v", label, r)
		}
	}
	if err := tt.Click("选择 SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionEditor.Draft().Kind != connections.SQLite {
		t.Fatal("minimum chooser did not navigate to the selected profile")
	}
}
