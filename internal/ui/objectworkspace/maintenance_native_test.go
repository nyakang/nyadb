package objectworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMain(m *testing.M) {
	if root := os.Getenv("NYADB_MAINTENANCE_NATIVE"); root != "" {
		if e := runMaintenanceNative(root); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}
func runMaintenanceNative(root string) error {
	if _, e := os.Stat(filepath.Join(root, ".owned")); e != nil {
		return e
	}
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	p := connections.Profile{ID: 1, Name: "SQLite native acceptance", Kind: connections.SQLite, Database: filepath.Join(root, "native.sqlite")}
	schema, action, extension := "main", "integrity_check", ".sqlite"
	destination := filepath.Join(root, "native-restored.sqlite")
	if port := os.Getenv("NYADB_NATIVE_POSTGRES_PORT"); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n < 1024 {
			return fmt.Errorf("invalid disposable PostgreSQL port")
		}
		p.Kind, p.Host, p.Port, p.Username, p.Database, p.TLSMode = connections.PostgreSQL, "127.0.0.1", uint16(n), "nyadb_it", "nyadb_it", "disable"
		p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
		p.PostgreSQLOptions.ToolsDirectory = os.Getenv("NYADB_POSTGRES_TOOLS")
		schema, action, extension, destination = "public", "ANALYZE", ".dump", "nyadb_native_restored"
	}
	session, e := manager.ConnectSession(context.Background(), p, "")
	if e != nil {
		return e
	}
	query, _ := contract.Require[contract.QueryProvider](session)
	lease, e := query.ReserveQuery(1)
	if e != nil {
		return e
	}
	_, e = lease.Execute(context.Background(), schema, "CREATE TABLE native_records(id INTEGER PRIMARY KEY,note TEXT); INSERT INTO native_records VALUES(1,'native UI')")
	lease.Release()
	if e != nil {
		return e
	}
	w := Session{ProfileID: 1, Generation: session.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return w }, SessionFor: func(int64) Session { return w }, Capabilities: func(int64) contract.Capabilities { return session.Capabilities() }, SchemaDesignFor: manager.SchemaDesign, SchemaTarget: func(id int64, db, table string) contract.SchemaDesignTarget {
		return contract.SchemaDesignTarget{Object: manager.Namespace(id, db).Object("table", table), Generation: manager.Generation(id)}
	}, MaintenanceFor: manager.Maintenance, BackupsFor: manager.Backups})
	defer s.CloseAll()
	s.SetBackupServices(nil, BackupFiles{})
	last := ""
	stop := make(chan struct{})
	defer close(stop)
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "NyaDB maintenance acceptance", Width: 1280, Height: 800, MinWidth: 840, MinHeight: 560, UseContentSize: true, Content: ui.View(func(c *ui.Context) {
			theme.Apply(c)
			services := c.Services()
			s.Consume(services)
			command, _ := os.ReadFile(filepath.Join(root, "command"))
			cmd := string(command)
			if cmd != last {
				last = cmd
				switch cmd {
				case "maintenance-plan":
					s.BeginMaintenance(services, 1, schema, "native_records", action)
				case "maintenance-run":
					s.executeMaintenance(services)
				case "backup-plan":
					s.closeMaintenance()
					s.BeginBackup(services, 1, schema, "", "backup")
					if x := s.portableBackup; x != nil {
						x.request.Path = filepath.Join(root, "native-backup"+extension)
						s.previewPortableBackup(services)
					}
				case "backup-run":
					s.executePortableBackup(services)
				case "restore-plan":
					s.closePortableBackup()
					s.BeginBackup(services, 1, schema, "", "restore")
					if x := s.portableBackup; x != nil {
						x.request.Path = filepath.Join(root, "native-backup"+extension)
						x.request.Destination = destination
						s.previewPortableBackup(services)
					}
				case "restore-run":
					s.executePortableBackup(services)
				case "overwrite-plan":
					s.closePortableBackup()
					s.BeginBackup(services, 1, schema, "", "restore")
					if x := s.portableBackup; x != nil {
						x.request.Path = filepath.Join(root, "native-backup"+extension)
						x.request.Destination, x.request.Overwrite = p.Database, true
						x.confirmation, x.acknowledgeSessions = p.Database, true
						s.previewPortableBackup(services)
					}
				case "overwrite-run":
					s.executePortableBackup(services)
				case "quit":
					mygo.App.Quit()
				}
			}
			ui.Column(c).Fill().Padding(24).Children(func() {
				ui.Text(c, "NyaDB · "+string(p.Kind)).Bold()
				ui.Text(c, "native_records · "+schema)
				s.Modals(c)
			})
			status := map[string]any{"command": last, "busy": false, "plan": false, "state": "", "error": ""}
			if x := s.maintenancePanel; x != nil {
				status["busy"], status["plan"], status["error"] = x.busy, x.plan != nil, x.err
				if x.outcome != nil {
					status["state"] = x.outcome.State
				}
			}
			if x := s.portableBackup; x != nil {
				status["busy"], status["plan"], status["error"] = x.busy, x.plan != nil, x.err
				if x.outcome != nil {
					status["state"] = x.outcome.State
				}
			}
			raw, _ := json.Marshal(status)
			_ = os.WriteFile(filepath.Join(root, "status.tmp"), raw, 0600)
			_ = os.Rename(filepath.Join(root, "status.tmp"), filepath.Join(root, "status.json"))
		})})
		go func() {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					window.Update(func() {})
				}
			}
		}()
	})
	if e = mygo.App.Run(); e != nil {
		return e
	}
	for _, file := range []string{destination, p.Database} {
		probe := p
		probe.ID, probe.Database = 2, file
		session, e := manager.ConnectSession(context.Background(), probe, "")
		if e != nil {
			return e
		}
		query, e := contract.Require[contract.QueryProvider](session)
		if e != nil {
			return e
		}
		lease, e := query.ReserveQuery(9)
		if e != nil {
			return e
		}
		result, e := lease.Execute(context.Background(), schema, "SELECT id,note FROM native_records")
		lease.Release()
		if e != nil || len(result.Rows) != 1 || result.Rows[0][0] != "1" || result.Rows[0][1] != "native UI" {
			return fmt.Errorf("native restored values differ: %v: %w", result.Rows, e)
		}
	}
	return os.WriteFile(filepath.Join(root, "maintenance-verified.json"), []byte(`{"new_target":true,"overwrite":true,"values":true}`), 0600)
}
