package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
)

func TestH2NativeNetworkPublicGridSQLAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_H2_NETWORK_SHELL_IT") != "1" {
		t.Skip("requires owned H2 network shell fixture")
	}
	var fixture struct {
		Archive, Directory, PasswordFile string
		Servers                          map[string]struct {
			TCPPort, SSLPort uint16
			CAFile           string
		}
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("network fixture unavailable")
	}
	secret, err := os.ReadFile(fixture.PasswordFile)
	if err != nil {
		t.Fatal("fixture credential unavailable")
	}
	password := strings.TrimSpace(string(secret))
	store, err := driverstore.New(filepath.Join(fixture.Directory, "network-shell-store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	driver, err := store.Import(ctx, driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}, fixture.Archive)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	observe := func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return err
		}
		args := strings.Split(string(cmd), "\x00")
		agent := ""
		for i, arg := range args {
			if arg == "-cp" && i+1 < len(args) {
				agent = strings.Split(args[i+1], string(os.PathListSeparator))[0]
			}
		}
		if len(fields) < 20 || agent == "" {
			return fmt.Errorf("original process identity missing")
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "java": args[0], "agent": agent})
		journal, err := os.OpenFile(filepath.Join(fixture.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		for _, mode := range []string{"tcp", "ssl"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				s, _, profiles, _ := profileTestState(t)
				vault := &memoryCredentialVault{secrets: map[string]string{}}
				s.SetCredentialVault(vault)
				s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
					return s.runtime.ConnectSession(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
				}, Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected})
				p, _ := connections.Defaults(connections.H2)
				p.Name, p.Host, p.Database, p.Username = "H2 "+profile+" "+mode, "127.0.0.1", "owned", "sa"
				p.H2Options.Mode, p.H2Options.DriverProfile, p.H2Options.AutoServer = mode, profile, false
				p.H2Options.DriverDirectory, p.H2Options.DriverVersion, p.H2Options.DriverSHA256 = driver.Directory, driver.Version, driver.SHA256
				p.H2Options.ConnectTimeoutSeconds = 120
				server := fixture.Servers[profile]
				p.Port = server.TCPPort
				if mode == "ssl" {
					p.Port = server.SSLPort
					p.H2Options.TLSCAFile = server.CAFile
				}
				p, err := profiles.Create(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if err = vault.Set(p.ID, "", password); err != nil {
					t.Fatal(err)
				}
				s.loadProfiles()
				s.selectProfileByName(p.Name)
				var services ui.Services
				tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
				wait := func(ready func() bool) {
					t.Helper()
					deadline := time.Now().Add(3 * time.Minute)
					for !ready() && time.Now().Before(deadline) {
						tt.Frame()
						time.Sleep(10 * time.Millisecond)
					}
					tt.Frame()
					if !ready() {
						t.Fatal("original native network UI request did not settle", s.sessions.Current().Error)
					}
				}
				if err := tt.Click("连接所选数据库"); err != nil {
					t.Fatal(err)
				}
				wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
				if !s.runtime.IsConnected(p.ID) {
					t.Fatal("original native network session unavailable", s.sessions.Current().Error)
				}
				generation := s.runtime.Generation(p.ID)
				if err := s.runtime.Test(driverstore.WithRuntimeObserver(context.Background(), observe), p, password); err != nil {
					t.Fatal("active network connection test failed", err)
				}
				if s.runtime.Generation(p.ID) != generation {
					t.Fatal("isolated test replaced original network generation")
				}
				s.openMySQLDatabase(services, "PUBLIC", true)
				wait(func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["PUBLIC"] != nil })
				s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "PUBLIC", Object: "network_records", Category: "表"})
				dataTab := s.currentTab()
				if dataTab.data == nil || dataTab.data.Object().Catalog == "" || dataTab.data.Object().Schema != "PUBLIC" {
					t.Fatal("network grid lost qualified namespace")
				}
				wait(func() bool { return !dataTab.data.Busy() })
				if dataTab.data.Error() != "" || dataTab.data.Page() == nil || dataTab.data.Page().Rows[0][0].Text != "9007199254740993" {
					t.Fatal("native network typed grid lost", dataTab.data.Error())
				}
				note := "saved network " + profile + " " + mode
				s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: note})
				tt.Frame()
				if err := tt.Click("保存数据更改"); err != nil {
					t.Fatal(err)
				}
				wait(func() bool { return !dataTab.data.Busy() })
				if dataTab.data.Error() != "" || dataTab.data.Page().Rows[0][1].Text != note {
					t.Fatal("native network grid save failed", dataTab.data.Error())
				}
				s.openMySQLStructure(services, "PUBLIC", "network_records")
				tt.Frame()
				structure := s.currentTab()
				wait(func() bool { return structure.object != nil && structure.object.Data() != nil })
				if structure.object.Data().Definition == "" {
					t.Fatal("native network structure lost")
				}
				s.openQuery()
				sqlTab := s.currentTab()
				sql := `SELECT "id","note" FROM "network_records"`
				sqlTab.query.Editor.Buffer.Set(sql)
				sqlTab.query.Editor.Select(0, len(sql))
				tt.Frame()
				wait(func() bool { return !sqlTab.query.Preparing() })
				if err := tt.Click("运行当前或选中 SQL"); err != nil {
					t.Fatal(err)
				}
				wait(func() bool { return !sqlTab.query.Loading() })
				result := sqlTab.query.Result()
				if result == nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 || result.Sets[0].Rows[0][0].Text != "9007199254740993" || result.Sets[0].Rows[0][1].Text != note {
					t.Fatal("native network SQL lost", sqlTab.query.Error())
				}
				if output := os.Getenv("NYADB_H2_NETWORK_SCREENSHOTS"); output != "" {
					if err := os.MkdirAll(output, 0700); err != nil {
						t.Fatal(err)
					}
					writeDomainCapture(t, output, profile+"-"+mode+"-sql", tt)
				}
				s.disconnectMySQLProfile(p.ID)
				tt.Frame()
				if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
					t.Fatal("network disconnect lost original SQL")
				}
				for _, tab := range s.tabs {
					if tab.profileID() == p.ID && (tab.data != nil || tab.object != nil) {
						t.Fatal("network disconnect retained scoped documents")
					}
				}
				s.sessions.Connect(p, services.Invalidate)
				wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
				if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) == generation {
					t.Fatal("explicit network reconnect failed")
				}
				if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
					t.Fatal(err)
				}
				wait(func() bool {
					return !sqlTab.query.NeedsRecovery() && !sqlTab.query.Preparing() && !sqlTab.query.Loading()
				})
				if sqlTab.query.Result() != nil || sqlTab.query.Error() != "" || sqlTab.query.Editor.Buffer.String() != sql {
					t.Fatal("explicit network recovery replayed SQL or failed", sqlTab.query.Error())
				}
			})
		}
	}
}
