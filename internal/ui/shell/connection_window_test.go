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
	if s.connectionStep != 1 || s.connectionEditor.Draft().Kind != connections.PostgreSQL ||
		s.ConnectionWindowTitle() != "新建连接 (PostgreSQL)" || w != 900 || h != 700 {
		t.Fatalf("native flow did not reach form: %+v", s.connectionEditor.Draft())
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
	s.ConnectionWindowClosed() // OS titlebar X
	if s.connectionDialog || s.connectionEditor.Active() {
		t.Fatal("OS close left modal/credential state behind")
	}
}

func TestNativeProfileEditingTitle(t *testing.T) {
	s := New()
	s.connectionStep = 1
	s.connectionEditor.OpenProfile(connections.Profile{ID: 18, Kind: connections.SQLite, Database: "/tmp/editing.sqlite"})
	if s.ConnectionWindowTitle() != "编辑连接 (SQLite)" {
		t.Fatal("editing must use native edit title")
	}
}

func TestConnectionEditorNativePickerCallbacksStayWithTheirSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(*connections.Profile)
		version    func(*State) uint64
		selectPath func(*State, uint64, string)
		path       func(*connections.Profile) string
	}{
		{"SSL", func(p *connections.Profile) {
			p.MySQLAdvanced.SSL.Enabled = true
			p.MySQLAdvanced.SSL.UseVerification = true
		}, (*State).MySQLSSLSessionVersion, func(s *State, version uint64, path string) {
			s.SelectMySQLSSLFile(MySQLSSLClientKey, version, path)
		}, func(p *connections.Profile) string { return p.MySQLAdvanced.SSL.ClientKey }},
		{"SSH", func(p *connections.Profile) {
			p.MySQLAdvanced.SSH.Enabled = true
			p.MySQLAdvanced.SSH.AuthMethod = connections.MySQLSSHPublicKey
		}, (*State).MySQLSSHSessionVersion, (*State).SelectMySQLSSHPrivateKey,
			func(p *connections.Profile) string { return p.MySQLAdvanced.SSH.PrivateKey }},
		{"HTTP", func(p *connections.Profile) {
			p.MySQLAdvanced.HTTP.Enabled = true
			p.MySQLAdvanced.HTTP.CertificateAuth = true
		}, (*State).MySQLHTTPSessionVersion, func(s *State, version uint64, path string) {
			s.SelectMySQLHTTPFile(MySQLHTTPClientKey, version, path)
		}, func(p *connections.Profile) string { return p.MySQLAdvanced.HTTP.ClientKey }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.openConnectionDialog()
			s.chooseConnectionKind(connections.MySQL)
			s.nextConnectionStep()
			tc.prepare(s.connectionEditor.Draft())
			version := tc.version(s)
			tc.selectPath(s, version, "/tmp/current.key")
			if tc.path(s.connectionEditor.Draft()) != "/tmp/current.key" {
				t.Fatal("native picker did not reach the current editor")
			}
			s.ConnectionWindowClosed()
			s.openConnectionDialog()
			s.chooseConnectionKind(connections.MySQL)
			s.nextConnectionStep()
			tc.prepare(s.connectionEditor.Draft())
			tc.selectPath(s, version, "/tmp/stale.key")
			if tc.version(s) == version || tc.path(s.connectionEditor.Draft()) != "" {
				t.Fatal("native completion from a closed editor reached the new session")
			}
		})
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
