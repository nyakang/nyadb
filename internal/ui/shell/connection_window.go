package shell

import "github.com/egoist/mygo/ui"

// SetConnectionWindowHost binds the native child-window lifecycle. When no
// host is installed (e.g. ui.NewTester), the legacy in-tree modal remains
// available for deterministic headless interaction tests.
func (s *State) SetConnectionWindowHost(open, close func()) {
	s.showConnectionWindow = open
	s.hideConnectionWindow = close
}

// ConnectionWindowClosed synchronizes state when the user closes the native
// child via its OS title-bar X/Alt-F4 instead of the in-app Cancel button.
func (s *State) ConnectionWindowClosed() {
	s.connectionDialog = false
	s.connectionSecret = ""
	s.connectionError = ""
}

func (s *State) ConnectionWindowTitle() string {
	if s.connectionDraft.ID != 0 {
		return "编辑连接"
	}
	return "新建连接"
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
