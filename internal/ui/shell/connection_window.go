package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/connectionicons"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
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
	s.connectionEditor.Close()
	s.connectionDialog = false
	s.connectionError = ""
}

func (s *State) ConnectionWindowTitle() string {
	if s.connectionStep == 1 {
		return s.connectionEditor.Title()
	}
	return locales.Text("新建连接")
}

func (s *State) ConnectionWindowSize() (int, int) {
	if s.connectionStep == 1 {
		return 900, 700
	}
	return 900, 700
}

// ConnectionWindowIcon uses the bundled, redistributable connection-type icon.
// The type chooser retains the default NyaDB application icon.
func (s *State) ConnectionWindowIcon() ([]byte, error) {
	if s.connectionStep != 1 {
		return nil, nil
	}
	var file string
	switch s.connectionEditor.Draft().Kind {
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
	// Native child windows do not inherit the workbench's theme.
	theme.Apply(c)
	if !s.connectionDialog {
		return
	}
	if !s.connectionEditor.InitialQueryOpen() && c.Shortcut(0, ui.KeyEscape) {
		s.closeConnectionDialog()
		return
	}
	if s.connectionStep == 0 {
		s.connectionTypes(c, true)
		return
	}
	s.connectionEditorView(c)
}

// SetCredentialVault shares the native vault with the editor and sessions.
func (s *State) SetCredentialVault(vault credentials.Vault) {
	s.credentialVault = vault
	s.connectionEditor.SetCredentialVault(vault)
}

// Aliases preserve bootstrap's native picker interface.
type MySQLSSLFile = connectioneditor.MySQLSSLFile
type MySQLHTTPFile = connectioneditor.MySQLHTTPFile

const (
	MySQLSSLClientKey          = connectioneditor.MySQLSSLClientKey
	MySQLSSLClientCertificate  = connectioneditor.MySQLSSLClientCertificate
	MySQLSSLCA                 = connectioneditor.MySQLSSLCA
	MySQLHTTPClientKey         = connectioneditor.MySQLHTTPClientKey
	MySQLHTTPClientCertificate = connectioneditor.MySQLHTTPClientCertificate
	MySQLHTTPCA                = connectioneditor.MySQLHTTPCA
)

func (s *State) SetMySQLSSLFilePicker(pick func(MySQLSSLFile)) {
	s.connectionEditor.SetMySQLSSLFilePicker(pick)
}

func (s *State) MySQLSSLSessionVersion() uint64 { return s.connectionEditor.MySQLSSLSessionVersion() }

func (s *State) SelectMySQLSSLFile(kind MySQLSSLFile, version uint64, path string) {
	s.connectionEditor.SelectMySQLSSLFile(kind, version, path)
}

func (s *State) MySQLSSLFilePickerError(version uint64) {
	s.connectionEditor.MySQLSSLFilePickerError(version)
}

func (s *State) SetMySQLSSHKeyPicker(pick func()) { s.connectionEditor.SetMySQLSSHKeyPicker(pick) }

func (s *State) MySQLSSHSessionVersion() uint64 { return s.connectionEditor.MySQLSSHSessionVersion() }

func (s *State) SelectMySQLSSHPrivateKey(version uint64, path string) {
	s.connectionEditor.SelectMySQLSSHPrivateKey(version, path)
}

func (s *State) MySQLSSHKeyPickerError(version uint64) {
	s.connectionEditor.MySQLSSHKeyPickerError(version)
}

func (s *State) SetMySQLHTTPFilePicker(fn func(MySQLHTTPFile)) {
	s.connectionEditor.SetMySQLHTTPFilePicker(fn)
}

func (s *State) MySQLHTTPSessionVersion() uint64 { return s.connectionEditor.MySQLHTTPSessionVersion() }

func (s *State) SelectMySQLHTTPFile(kind MySQLHTTPFile, version uint64, path string) {
	s.connectionEditor.SelectMySQLHTTPFile(kind, version, path)
}

func (s *State) MySQLHTTPFilePickerError(version uint64) {
	s.connectionEditor.MySQLHTTPFilePickerError(version)
}

func (s *State) SetMySQLSettingsPicker(pick func()) { s.connectionEditor.SetMySQLSettingsPicker(pick) }

func (s *State) SelectMySQLSettingsDirectory(path string) {
	s.connectionEditor.SelectMySQLSettingsDirectory(path)
}

func (s *State) MySQLSettingsPickerError() { s.connectionEditor.MySQLSettingsPickerError() }

func (s *State) SetSQLiteFilePicker(pick func()) { s.connectionEditor.SetSQLiteFilePicker(pick) }

func (s *State) SelectSQLiteFile(path string) { s.connectionEditor.SelectSQLiteFile(path) }

func (s *State) SQLiteFilePickerError() { s.connectionEditor.SQLiteFilePickerError() }
