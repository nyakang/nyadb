package shell

import (
	"bytes"
	"image/png"
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
	w, h := s.ConnectionWindowSize()
	if w != 900 || h != 700 {
		t.Fatalf("chooser should use the observed 900×700 client size, got %d×%d", w, h)
	}
	tt := ui.NewTester(s.ConnectionWindowView, w, h)
	if !tt.HasText("连接筛选") || !tt.HasText("全部") {
		t.Fatal("native chooser view missing")
	}
	if err := tt.Click("选择 PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	w, h = s.ConnectionWindowSize()
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

func TestConnectionWindowIconFollowsSessionType(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	if data, err := s.ConnectionWindowIcon(); err != nil || data != nil {
		t.Fatalf("chooser should keep the NyaDB app icon: %d bytes, %v", len(data), err)
	}
	var icons [][]byte
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
		s.chooseConnectionKind(kind)
		s.nextConnectionStep()
		data, err := s.ConnectionWindowIcon()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatalf("%s window icon is not a valid PNG: %v", kind, err)
		}
		for _, other := range icons {
			if bytes.Equal(other, data) {
				t.Fatalf("%s unexpectedly shares another session icon", kind)
			}
		}
		icons = append(icons, data)
		s.connectionStep = 0
		if data, err := s.ConnectionWindowIcon(); err != nil || data != nil {
			t.Fatalf("returning to chooser must restore default icon: %v", err)
		}
	}
}
