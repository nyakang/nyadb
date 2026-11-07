package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/dameng/dpiworker"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// A separate native test binary hosts the real Shell toolbar at 420 pixels.
// Formal bootstrap keeps its 840 pixel minimum. This is component evidence.
func TestMain(m *testing.M) {
	if dpiworker.Dispatch() {
		return
	}
	if root := os.Getenv("NYADB_UX_NATIVE"); root != "" {
		if err := runWorkflowNativeFixture(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if os.Getenv("NYADB_ENTRY_NATIVE") == "1" {
		if err := runEntryNativeFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runEntryNativeFixture() error {
	root := os.Getenv("NYADB_ENTRY_ROOT")
	if os.Getenv("NYADB_ENTRY_IT") != "1" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "unix:path="+root+"/runtime/bus" {
		return fmt.Errorf("private fixture required")
	}
	if _, e := os.Stat(filepath.Join(root, ".owned")); e != nil {
		return fmt.Errorf("owned fixture required")
	}
	db, e := localdb.Open(filepath.Join(root, "config/NyaDB/config.sqlite"))
	if e != nil {
		return e
	}
	defer db.Close()
	svc := connections.NewService(localdb.NewProfileStore(db))
	profiles, e := svc.List(context.Background())
	if e != nil {
		return e
	}
	var profile connections.Profile
	for _, p := range profiles {
		if p.Username == "entry_admin" {
			profile = p
		}
	}
	if profile.ID == 0 || profile.Host != "127.0.0.1" {
		return fmt.Errorf("owned administrator missing")
	}
	s := NewWithProfiles(svc)
	defer s.CloseMySQLSessions()
	s.SetCredentialVault(credentials.NewOSVault())
	s.selectProfileByName(profile.Name)
	password, found, e := credentials.NewOSVault().Get(profile.ID, "")
	if e != nil || !found {
		return fmt.Errorf("real vault credential unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if e = s.runtime.Connect(ctx, profile, password); e != nil {
		return fmt.Errorf("real fixture connect failed")
	}
	w := s.sessions.Current()
	w.ProfileID = profile.ID
	w.Status = "已连接"
	w.Generation = s.runtime.Generation(profile.ID)
	w.Database = "entry_source"
	s.sessions.MarkOpened(profile.ID, "entry_source")
	s.SetBackupFiles(objectworkspace.BackupFiles{Save: func(string) (string, error) { return filepath.Join(root, "evidence/narrow.nyabackup"), nil }, Write: queries.WriteStream})
	theme.SetMode(theme.ModeDark)
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{Title: "NyaDB toolbar fixture", Width: 420, Height: 320, MinWidth: 420, MinHeight: 320, UseContentSize: true, Content: ui.View(func(c *ui.Context) {
			theme.Apply(c)
			ui.Column(c).Fill().Children(func() { ui.Text(c, "原生 Shell 工具栏组件验收（420 像素）"); s.toolBar(c) })
		})})
	})
	return mygo.App.Run()
}
