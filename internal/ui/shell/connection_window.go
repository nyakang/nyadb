package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/connectionicons"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// SetConnectionWindowHost binds the native child-window lifecycle. When no
// host is installed (e.g. ui.NewTester), the legacy in-tree modal remains
// available for deterministic headless interaction tests.
func (s *State) SetConnectionWindowHost(open, close func()) {
	s.showConnectionWindow = open
	s.hideConnectionWindow = close
}

// SetConnectionWindowTitleUpdater synchronizes the native window title.
func (s *State) SetConnectionWindowTitleUpdater(update func(string)) {
	s.setConnectionTitle = update
}

func (s *State) updateConnectionWindowTitle() {
	if s.setConnectionTitle != nil {
		s.setConnectionTitle(s.ConnectionWindowTitle())
	}
}

// ConnectionWindowClosed synchronizes state when the user closes the native
// child via its OS title-bar X/Alt-F4 instead of the in-app Cancel button.
func (s *State) ConnectionWindowClosed() {
	s.connectionDialog = false
	s.resetConnectionConfigurations()
	s.connectionSecret = ""
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
}

func (s *State) ConnectionWindowTitle() string {
	prefix := "新建连接"
	if s.connectionDraft.ID != 0 {
		prefix = "编辑连接"
	}
	if s.connectionStep == 1 && s.connectionDraft.Kind != "" {
		if s.connectionDraft.ID != 0 && s.connectionDraft.Kind == connections.MySQL {
			return s.connectionMainConfig.profile.Name + " - 编辑连接 (MySQL)"
		}
		return prefix + " (" + connectionKindName(s.connectionDraft.Kind) + ")"
	}
	return prefix
}

func (s *State) ConnectionWindowSize() (int, int) {
	if s.connectionStep == 1 {
		return 900, 700
	}
	return 902, 732
}

// ConnectionWindowIcon uses the bundled, redistributable connection-type icon.
// The type chooser retains the default NyaDB application icon.
func (s *State) ConnectionWindowIcon() ([]byte, error) {
	if s.connectionStep != 1 {
		return nil, nil
	}
	var file string
	switch s.connectionDraft.Kind {
	case connections.MySQL:
		file = "mysql.png"
	case connections.PostgreSQL:
		file = "postgresql.png"
	case connections.SQLite:
		file = "sqlite.png"
	default:
		return nil, nil
	}
	return connectionicons.Read(file)
}

// ConnectionWindowView is the full content of a parent-owned, native modal
// window. It is not a WebView, ui.Modal overlay or fake window chrome.
func (s *State) ConnectionWindowView(c *ui.Context) {
	if !s.connectionDialog {
		return
	}
	if s.connectionStep == 0 {
		s.connectionTypes(c, true)
		return
	}
	s.connectionEditor(c)
}
