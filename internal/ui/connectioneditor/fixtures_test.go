package connectioneditor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type testEditor struct {
	*State
	connectionDialog  bool
	connectionStep    int
	connectionKind    connections.Kind
	selectedProfileID int64
	profileEntries    []connections.Profile
}

func newTestEditor() *testEditor {
	s := &testEditor{}
	s.State = New(Dependencies{
		ValidateMySQL: func(st connections.ActiveSettings, secret string) error {
			_, err := mysqlsvc.DirectMySQLConfig(st, secret)
			return err
		},
		TestMySQL: mysqlsvc.TestDirectMySQLConnection,
		Saved: func(p connections.Profile) {
			s.connectionDialog = false
			s.selectedProfileID = p.ID
			s.loadProfiles()
		},
		Cancel: func() { s.connectionDialog = false },
		Back:   func() { s.connectionStep = 0 },
	})
	return s
}

func profileTestState(t *testing.T) (*testEditor, *ui.Tester, *connections.Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service := connections.NewService(localdb.NewProfileStore(db))
	s := newTestEditor()
	s.SetProfileService(service)
	return s, ui.NewTester(s.View, 1280, 800), service, path
}

func (s *testEditor) openConnectionDialog() {
	s.Close()
	s.connectionDialog = true
	s.connectionStep = 0
}
func (s *testEditor) chooseConnectionKind(kind connections.Kind) { s.connectionKind = kind }
func (s *testEditor) nextConnectionStep() {
	if err := s.OpenNew(s.connectionKind, ""); err != nil {
		panic(err)
	}
	s.connectionDialog = true
	s.connectionStep = 1
}
func (s *testEditor) closeConnectionDialog()  { s.Close(); s.connectionDialog = false }
func (s *testEditor) ConnectionWindowClosed() { s.closeConnectionDialog() }
func (s *testEditor) saveConnectionDraft()    { s.Save() }
func (s *testEditor) loadProfiles() {
	if s.profileService != nil {
		s.profileEntries, _ = s.profileService.List(context.Background())
	}
}
func (s *testEditor) startEditProfile() {
	p, err := s.profileService.Get(context.Background(), s.selectedProfileID)
	if err != nil {
		panic(err)
	}
	s.OpenProfile(p)
	s.connectionDialog = true
	s.connectionStep = 1
}
func (s *testEditor) SetConnectionWindowHost(open, close func()) {}
func (s *testEditor) ConnectionWindowTitle() string              { return s.Title() }
func (s *testEditor) View(c *ui.Context)                         { s.ConnectionWindowView(c) }
func (s *testEditor) ConnectionWindowView(c *ui.Context) {
	theme.Apply(c)
	if !s.connectionDialog {
		return
	}
	if !s.InitialQueryOpen() && c.Shortcut(0, ui.KeyEscape) {
		s.Cancel()
		return
	}
	width, height := c.Size()
	s.State.View(c, width, height)
}
