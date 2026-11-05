package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"os"
	"path/filepath"
	"time"
)

// Native acceptance uses an owned disposable SQLite file and the real runtime.
// Fixture commands open workspaces; buttons and menus remain normal native UI.
func runWorkflowNativeFixture(root string) error {
	if _, err := os.Stat(filepath.Join(root, ".owned")); err != nil {
		return fmt.Errorf("owned workflow fixture required: %w", err)
	}
	db, err := localdb.Open(filepath.Join(root, "config.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	svc := connections.NewService(localdb.NewProfileStore(db))
	p, err := svc.Create(context.Background(), connections.Profile{Name: "UX SQLite", Kind: connections.SQLite, Database: filepath.Join(root, "fixture.sqlite"), SQLiteOptions: connections.DefaultSQLiteOptions()})
	if err != nil {
		return err
	}
	s := NewWithProfiles(svc)
	defer s.CloseMySQLSessions()
	s.selectProfileByName(p.Name)
	theme.SetMode(theme.ModeLight)
	mygo.Theme.SetSource(mygo.ThemeLight)
	mygo.App.SetLocale(os.Getenv("NYADB_UX_LOCALE"))
	last := ""
	stop := make(chan struct{})
	defer close(stop)
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "NyaDB UX acceptance", Width: 1280, Height: 800, MinWidth: 840, MinHeight: 560, UseContentSize: true, Content: ui.View(func(c *ui.Context) {
			services := c.Services()
			command, _ := os.ReadFile(filepath.Join(root, "command"))
			cmd := string(command)
			if cmd != last {
				last = cmd
				switch cmd {
				case "connect":
					s.sessions.Connect(p, services.Invalidate)
				case "open":
					s.openMySQLDatabase(services, "main", false)
				case "data":
					s.openMySQLData(services, "main", "customers")
				case "sql":
					s.openQuery()
					d := s.currentTab().query
					d.Editor.Buffer.Set("SELECT id, name, email FROM customers LIMIT 5;\nSELECT id, status FROM orders LIMIT 3;")
					s.sqlWorkspace.RunAll(services.Invalidate, d)
				case "rerun":
					if d := s.currentTab().query; d != nil {
						d.Editor.Buffer.Set("SELECT id, name, email FROM customers LIMIT 2;")
						s.sqlWorkspace.RunAll(services.Invalidate, d)
					}
				case "design":
					s.openMySQLStructure(services, "main", "customers", true)
				case "quit":
					mygo.App.Quit()
				}
			}
			s.View(c)
			t := s.currentTab()
			status := map[string]any{"command": last, "connected": s.sessions.Current().Status == "已连接", "database": s.sessions.Current().Database, "pending": s.sessions.Current().Pending, "kind": t.kind, "status": s.status}
			if t.data != nil {
				status["busy"] = t.data.Busy()
				status["rows"] = 0
				if page := t.data.Page(); page != nil {
					status["rows"] = len(page.Rows)
				}
				status["draft"] = t.data.HasDraft()
				status["error"] = t.data.Error()
			}
			if t.query != nil {
				status["busy"] = t.query.Loading()
				status["result"] = t.query.Result() != nil
				status["error"] = t.query.Error()
			}
			if t.object != nil {
				status["busy"] = t.object.DesignBusy()
				status["design"] = t.object.DesignMode()
				status["error"] = t.object.Error()
			}
			b, _ := json.Marshal(status)
			_ = os.WriteFile(filepath.Join(root, "status.json"), b, 0600)
		})})
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					window.Invalidate()
				case <-stop:
					return
				}
			}
		}()
	})
	return mygo.App.Run()
}
