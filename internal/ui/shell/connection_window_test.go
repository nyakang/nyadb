package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeConnectionWindowLifecycleAndFallback(t *testing.T) {
	s := New()
	var opens, closes int
	s.SetConnectionWindowHost(func() { opens++ }, func() { closes++ })
	s.openConnectionDialog()
	if opens != 1 || !s.connectionDialog || s.ConnectionWindowTitle() != "新建连接" {
		t.Fatalf("native host not opened: %d %v", opens, s.connectionDialog)
	}
	// The real window paints chooser content, not the main app's ui.Modal.
	tt := ui.NewTester(s.ConnectionWindowView, 902, 700)
	if !tt.HasText("连接筛选") || !tt.HasText("全部") {
		t.Fatal("native chooser view missing")
	}
	if err := tt.Click("选择 PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	w, h := s.ConnectionWindowSize()
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.PostgreSQL ||
		s.ConnectionWindowTitle() != "新建连接 (PostgreSQL)" || w != 900 || h != 700 {
		t.Fatalf("native flow did not reach form: %+v", s.connectionDraft)
	}
	if err := tt.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if closes != 1 || s.connectionDialog {
		t.Fatal("native window did not close")
	}
	s.openConnectionDialog()
	if opens != 2 {
		t.Fatal("second native launch missing")
	}
	s.connectionSecret = "temporary-secret"
	s.ConnectionWindowClosed() // OS titlebar X
	if s.connectionDialog || s.connectionSecret != "" {
		t.Fatal("OS close left modal/credential state behind")
	}
}

func TestNativeProfileEditingTitle(t *testing.T) {
	s := New()
	s.connectionDraft.ID = 18
	if s.ConnectionWindowTitle() != "编辑连接" {
		t.Fatal("editing must use native edit title")
	}
}
