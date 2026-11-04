package dataworkspace

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
	if root := os.Getenv("NYADB_IMPORT_NATIVE"); root != "" {
		if e := runImportNative(root); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}
func runImportNative(root string) error {
	if _, e := os.Stat(filepath.Join(root, ".owned")); e != nil {
		return e
	}
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	p := connections.Profile{ID: 1, Name: "SQLite import acceptance", Kind: connections.SQLite, Database: filepath.Join(root, "native-import.sqlite")}
	schema := "main"
	if port := os.Getenv("NYADB_NATIVE_POSTGRES_PORT"); port != "" {
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n < 1024 {
			return fmt.Errorf("invalid disposable PostgreSQL port")
		}
		p.Kind, p.Host, p.Port, p.Username, p.Database, p.TLSMode = connections.PostgreSQL, "127.0.0.1", uint16(n), "nyadb_it", "nyadb_it", "disable"
		schema = "public"
	}
	connected, e := manager.ConnectSession(context.Background(), p, "")
	if e != nil {
		return e
	}
	w := Session{ProfileID: 1, Generation: connected.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return w }, SessionFor: func(int64) Session { return w }, Runtime: func() Runtime { return manager.Data(1) }, RuntimeFor: manager.Data})
	defer s.CloseAll()
	d := s.OpenImport(1, 1, schema)
	d.transfer.NewName = "imported_records"
	input := filepath.Join(root, "source.csv")
	if e = os.WriteFile(input, []byte("id,note,amount\n1,native import,9007199254740993.123456789\n2,second row,0.01\n"), 0600); e != nil {
		return e
	}
	last := ""
	stop := make(chan struct{})
	defer close(stop)
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "NyaDB import acceptance", Width: 1280, Height: 800, MinWidth: 840, MinHeight: 560, UseContentSize: true, Content: ui.View(func(c *ui.Context) {
			theme.Apply(c)
			services := c.Services()
			s.Consume(services)
			command, _ := os.ReadFile(filepath.Join(root, "command"))
			cmd := string(command)
			if cmd != last {
				last = cmd
				switch cmd {
				case "import-read":
					s.readDataImportPath(services, d, input)
				case "import-plan":
					d.transfer.Step = 2
					d.transfer.Confirmation = d.transfer.NewName
					s.prepareDataImport(services, d)
				case "import-run":
					d.transfer.Step = 4
					s.startPreparedDataImport(services, d)
				case "quit":
					mygo.App.Quit()
				}
			}
			s.View(c, d)
			s.Modals(c)
			status := map[string]any{"command": last, "busy": d.transfer.Busy, "plan": d.transfer.MappedSpool != nil, "input": d.transfer.Input != nil, "completed": d.transfer.Completed, "error": d.transfer.Err}
			var prepared, failed int64
			for _, receipt := range d.transfer.Batch {
				if receipt.Result != nil {
					if receipt.Result.Committed {
						prepared += receipt.Result.Progress.Prepared
					}
					failed += receipt.Result.Progress.Failed
				}
			}
			status["processed"], status["prepared"], status["failed"] = d.transfer.Processed, prepared, failed
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
	query, e := contract.Require[contract.QueryProvider](connected)
	if e != nil {
		return e
	}
	lease, e := query.ReserveQuery(2)
	if e != nil {
		return e
	}
	defer lease.Release()
	result, e := lease.Execute(context.Background(), schema, "SELECT id,note,amount FROM imported_records ORDER BY id")
	if e != nil || len(result.Rows) != 2 || result.Rows[0][0] != "1" || result.Rows[0][2] != "9007199254740993.123456789" || result.Rows[1][0] != "2" || result.Rows[1][2] != "0.01" {
		return fmt.Errorf("native imported values differ: %v: %w", result.Rows, e)
	}
	return os.WriteFile(filepath.Join(root, "import-verified.json"), []byte(`{"rows":2,"exact_values":true}`), 0600)
}
