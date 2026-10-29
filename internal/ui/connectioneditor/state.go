// Package connectioneditor owns connection form drafts and their lifecycle.
// The workbench supplies services and host callbacks; no storage driver or
// database session is owned by this package.
package connectioneditor

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/locales"
)

// Dependencies binds configuration, vault and connection-test services to the
// editor. Host callbacks run on the UI thread after transient secrets and
// pending work have been cleared.
type Dependencies struct {
	Profiles      *connections.Service
	Vault         credentials.Vault
	ValidateMySQL func(connections.ActiveSettings, string) error
	TestMySQL     func(context.Context, connections.ActiveSettings, string) error
	Saved         func(connections.Profile)
	Back          func()
	Cancel        func()
}

// State is owned by the UI thread. Background tests return immutable results;
// native picker completions are guarded by the editing session's version.
type State struct {
	connectionEditorTab          int
	connectionDraft              connections.Profile
	connectionPort               string
	mysqlAdvancedNumbers         [5]string
	mysqlCompatibilityVisible    bool
	mysqlDatabaseTable           ui.ListState
	mysqlInitialQuery            string
	mysqlInitialQueryPreview     string
	mysqlInitialQueryDraft       string
	mysqlInitialQueryOpen        bool
	mysqlInitialQueryError       string
	mysqlSSLPassphrase           string
	mysqlSSLSessionVersion       uint64
	mysqlSSHPort                 string
	mysqlSSHPassword             string
	mysqlSSHPassphrase           string
	mysqlSSHSessionVersion       uint64
	mysqlHTTPSubtab              int
	mysqlHTTPProxyPort           string
	mysqlHTTPPassword            string
	mysqlHTTPProxyPassword       string
	mysqlHTTPPassphrase          string
	mysqlHTTPSessionVersion      uint64
	connectionSecret             string
	connectionSavePassword       bool
	credentialVault              credentials.Vault
	connectionPasswordLoadFailed bool
	connectionError              string
	connectionProfilesVisible    bool
	connectionActiveConfig       int
	connectionActiveTarget       int // Active for future sessions, independent of highlighted row.
	connectionMainConfig         connectionEditorSnapshot
	connectionOtherConfigs       []connectionEditorSnapshot

	connectionNameFocusPending bool

	connectionTesting        bool
	connectionTestMessage    string
	connectionTestGeneration uint64
	connectionTestCancel     context.CancelFunc
	connectionTestResults    <-chan mysqlTestResult
	mysqlConnectionTester    mysqlConnectionTester

	sqliteFilePicker    func()
	mysqlSettingsPicker func()
	mysqlSSLFilePicker  func(MySQLSSLFile)
	mysqlSSHKeyPicker   func()
	mysqlHTTPFilePicker func(MySQLHTTPFile)
	active              bool
	profileService      *connections.Service
	validateMySQL       func(connections.ActiveSettings, string) error
	saved               func(connections.Profile)
	back                func()
	canceled            func()
}

func New(deps Dependencies) *State {
	return &State{profileService: deps.Profiles, credentialVault: deps.Vault,
		validateMySQL: deps.ValidateMySQL, mysqlConnectionTester: deps.TestMySQL,
		saved: deps.Saved, back: deps.Back, canceled: deps.Cancel}
}

func (s *State) SetProfileService(service *connections.Service) { s.profileService = service }

// Draft returns the current, non-secret profile draft. Raw numeric inputs and
// all passwords remain separate. Do not retain the draft across editor opens.
func (s *State) Draft() *connections.Profile { return &s.connectionDraft }
func (s *State) Error() string               { return s.connectionError }
func (s *State) Active() bool                { return s.active }
func (s *State) InitialQueryOpen() bool      { return s.mysqlInitialQueryOpen }

// OpenNew starts a fresh draft with the selected database's defaults.
func (s *State) OpenNew(kind connections.Kind, group string) error {
	s.Close()
	draft, err := connections.Defaults(kind)
	if err != nil {
		return err
	}
	draft.GroupName = group
	s.connectionDraft = draft
	s.initializeInputs()
	s.connectionSavePassword = kind == connections.MySQL && s.credentialVault != nil
	s.connectionEditorTab = 0
	s.connectionNameFocusPending = true
	s.active = true
	return nil
}

// OpenProfile restores saved configurations and vault passwords for editing.
func (s *State) OpenProfile(profile connections.Profile) {
	s.Close()
	s.connectionDraft = profile
	s.initializeInputs()
	s.connectionNameFocusPending = true
	s.connectionEditorTab = 0
	s.active = true
	s.initializeConnectionConfigurations()
	if profile.Kind == connections.MySQL {
		s.loadSavedConnectionPasswords()
	}
}

func (s *State) initializeInputs() {
	if s.connectionDraft.Kind == connections.MySQL {
		s.initMySQLAdvancedInputs()
		s.initMySQLSSHInputs()
		s.initMySQLHTTPInputs()
	}
	s.connectionPort = strconv.Itoa(int(s.connectionDraft.Port))
	if s.connectionDraft.Kind == connections.SQLite {
		s.connectionPort = ""
	}
}

// Close discards configuration drafts and secrets and invalidates async work.
// The non-secret profile remains readable until the next Open call.
func (s *State) Close() {
	s.cancelMySQLConnectionTest()
	s.resetConnectionConfigurations()
	s.active = false
	s.connectionSecret = ""
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
}

func (s *State) Cancel() {
	s.Close()
	if s.canceled != nil {
		s.canceled()
	}
}

func (s *State) Back() {
	s.cancelMySQLConnectionTest()
	s.active = false
	s.connectionEditorTab = 0
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
	if s.back != nil {
		s.back()
	}
}

func (s *State) Title() string {
	prefix := "新建连接"
	if s.connectionDraft.ID != 0 {
		prefix = "编辑连接"
	}
	if s.active && s.connectionDraft.Kind != "" {
		if s.connectionDraft.ID != 0 && s.connectionDraft.Kind == connections.MySQL {
			return s.connectionMainConfig.profile.Name + " - " + locales.Text("编辑连接") + " (MySQL)"
		}
		return locales.Text(prefix) + " (" + connectionKindName(s.connectionDraft.Kind) + ")"
	}
	return locales.Text(prefix)
}

func connectionKindName(kind connections.Kind) string {
	switch kind {
	case connections.MySQL:
		return "MySQL"
	case connections.PostgreSQL:
		return "PostgreSQL"
	case connections.SQLite:
		return "SQLite"
	default:
		return "未知类型"
	}
}
