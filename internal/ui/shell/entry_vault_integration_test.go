package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

// Separate process: no keyring.MockInit is executed while Secret Service is down.
func TestEntryAcceptanceVaultUnavailableKeepsTextAndAllowsExplicitFile(t *testing.T) {
	if os.Getenv("NYADB_ENTRY_KEYRING_DOWN") != "1" {
		t.Skip("requires stopped private Secret Service")
	}
	root := os.Getenv("NYADB_ENTRY_ROOT")
	if os.Getenv("NYADB_ENTRY_IT") != "1" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "unix:path="+root+"/runtime/bus" {
		t.Fatal("private fixture required")
	}
	if _, e := os.Stat(filepath.Join(root, ".owned")); e != nil {
		t.Fatal("owned fixture required")
	}
	db, e := localdb.Open(filepath.Join(root, "config/vault.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	defer s.CloseMySQLSessions()
	path := filepath.Join(root, "evidence/unavailable-ui.sql")
	paths := make(chan string, 2)
	paths <- ""
	paths <- path
	s.SetSQLPersistence(queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{}), sqlworkspace.Files{SavePath: func(string) (string, error) { return <-paths, nil }, Write: queries.WriteFile}, nil)
	tt := ui.NewTester(s.View, 1280, 800)
	deadline := time.Now().Add(10 * time.Second)
	for s.sqlWorkspace.PersistenceLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if s.sqlWorkspace.PersistenceLoading() {
		t.Fatal("unavailable service did not settle")
	}
	s.openQuery()
	tt.Frame()
	doc := s.currentTab().query
	const text = "SELECT 42; -- typed while Secret Service is unavailable"
	doc.Editor.Select(0, doc.Editor.Buffer.Len())
	doc.Editor.Insert(text)
	tt.Frame()
	if e = tt.Click("保存查询"); e != nil {
		t.Fatal(e)
	}
	// Cancel naming after an unavailable store; explicit file save remains usable.
	if tt.HasText("保存命名查询") {
		if e = tt.Click("取消"); e != nil {
			t.Fatal(e)
		}
		tt.Frame()
	}
	for i := 0; i < 20; i++ {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if doc.Editor.Buffer.String() != text || !doc.Dirty() {
		t.Fatal("failed persistence lost text or dirty state")
	}
	if e = tt.Click("另存为"); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if _, e = os.Stat(path); e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, e := queries.ReadFile(path)
	if e != nil || got != text {
		t.Fatal("explicit file fallback failed")
	}
	if doc.Editor.Buffer.String() != text {
		t.Fatal("file save lost text")
	}
}
