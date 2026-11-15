package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
)

func TestH2NativePublicChooserURISharedGridSQLStructureAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_H2_SHELL_IT") != "1" {
		t.Skip("requires owned managed H2 shell fixture")
	}
	var f struct{ Archive, SHA256, Directory, PasswordFile string }
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("managed fixture unavailable")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("fixture credential unavailable")
	}
	password := strings.TrimSpace(string(secret))
	if output := os.Getenv("NYADB_H2_SHELL_SCREENSHOTS"); output != "" {
		if err := os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := driverstore.New(filepath.Join(f.Directory, "shell-store"))
	if err != nil {
		t.Fatal(err)
	}
	product := driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}
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
			return fmt.Errorf("original process identity unavailable")
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "agent": agent, "java": args[0]})
		file, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(record, '\n'))
		return err
	}
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		t.Run(profile, func(t *testing.T) {
			s, _, service, _ := profileTestState(t)
			t.Cleanup(s.CloseMySQLSessions)
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			s.SetCredentialVault(vault)
			s.SetDriverPackages(connectioneditor.DriverPackages{
				List: func(ctx context.Context, kind connections.Kind, dir string) ([]connections.ManagedDriver, error) {
					return store.List(ctx, product)
				},
				Import: func(ctx context.Context, kind connections.Kind, dir, file string) (connections.ManagedDriver, error) {
					return store.Import(ctx, product, file)
				},
			})
			// Observe the request's original process without replacing any builtin provider.
			s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{
				Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
					return s.runtime.ConnectSession(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
				},
				Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected,
			})
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			ct := ui.NewTester(s.ConnectionWindowView, 920, 760)
			input := func(tt *ui.Tester, label, value string) {
				t.Helper()
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Command("selectAll")
				tt.Type(value)
			}
			wait := func(tt *ui.Tester, ready func() bool) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Minute)
				for !ready() && time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(10 * time.Millisecond)
				}
				tt.Frame()
				if !ready() {
					t.Fatal("original native UI request did not settle", s.sessions.Current().Error)
				}
			}
			input(ct, "搜索连接类型", "H2")
			if err := ct.Click("选择 H2"); err != nil {
				t.Fatal(err)
			}
			if err := ct.Click("下一步"); err != nil {
				t.Fatal(err)
			}
			if s.connectionEditor.Draft().Kind != connections.H2 {
				t.Fatal("public chooser did not open H2 editor")
			}
			picked := false
			s.SetEngineFilePicker(func(kind EngineFile, row int, version uint64) {
				if kind == H2DriverPackageFile {
					picked = true
					s.SelectEngineFile(kind, row, version, f.Archive)
				}
			})
			wait(ct, func() bool {
				if !picked {
					_ = ct.Click("导入驱动包")
				}
				return picked
			})
			wait(ct, func() bool { return s.connectionEditor.Draft().H2Options.DriverSHA256 == f.SHA256 })
			draft := *s.connectionEditor.Draft()
			draft.H2Options.DriverProfile = profile
			s.connectionEditor.OpenProfile(draft)
			originalPin := s.connectionEditor.Draft().H2Options
			s.connectionEditor.BeginURIFromEditor()
			database := filepath.Join(f.Directory, "public-"+profile)
			uri := "jdbc:h2:file:" + filepath.ToSlash(database) + ".mv.db;IFEXISTS=FALSE;USER=sa;PASSWORD=" + url.PathEscape(password)
			if err := s.connectionEditor.ImportURI(uri); err != nil {
				t.Fatal(err)
			}
			a := s.connectionEditor.Draft().H2Options
			if a.DriverProfile != profile || a.DriverVersion != originalPin.DriverVersion || a.DriverSHA256 != originalPin.DriverSHA256 || a.DriverDirectory != originalPin.DriverDirectory || !a.AutoServer {
				t.Fatal("URI lost original fixed driver identity or shared file semantics")
			}
			input(ct, "连接名称", "H2 public "+profile)
			if err := ct.Click("保存密码"); err != nil {
				t.Fatal(err)
			}
			if err := ct.Click("高级"); err != nil {
				t.Fatal(err)
			}
			input(ct, "连接超时（秒）", "120")
			if err := ct.Click("确定"); err != nil {
				t.Fatal(err)
			}
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 {
				t.Fatal("public H2 configuration was not saved", err)
			}
			p := profiles[0]
			savedSecret, savedPassword, savedErr := vault.Get(p.ID, "")
			if p.Database != database || !savedPassword || savedErr != nil || savedSecret != password {
				t.Fatal("original file path/password save lost")
			}
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
			s.selectProfileByName(p.Name)
			tt.Frame()
			if err := tt.Click("连接所选数据库"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
			if !s.runtime.IsConnected(p.ID) || s.sessions.Current().Kind != connections.H2 {
				t.Fatal("builtin H2 did not publish original session")
			}
			session, err := s.runtime.Session(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			query, err := contract.Require[contract.QueryProvider](session)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := query.ReserveQuery(991)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if _, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "public_records"("id" BIGINT PRIMARY KEY,"note" VARCHAR); INSERT INTO "public_records" VALUES(9007199254740993,'original shared grid')`); err != nil {
				t.Fatal(err)
			}
			generation := s.runtime.Generation(p.ID)
			originalID, err := lease.Execute(ctx, "PUBLIC", `SELECT SESSION_ID()`)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.runtime.Test(driverstore.WithRuntimeObserver(ctx, observe), p, password); err != nil {
				t.Fatal("test of already-open native file failed", err)
			}
			afterID, err := lease.Execute(ctx, "PUBLIC", `SELECT SESSION_ID()`)
			if err != nil || afterID.Sets[0].Rows[0][0].Text != originalID.Sets[0].Rows[0][0].Text || s.runtime.Generation(p.ID) != generation {
				t.Fatal("connection test replaced original published generation/physical lease", err)
			}
			s.sessions.RefreshDatabases(services.Invalidate)
			wait(tt, func() bool { return !s.sessions.Current().Refreshing() })
			s.openMySQLDatabase(services, "PUBLIC", true)
			wait(tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["PUBLIC"] != nil })
			s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "PUBLIC", Object: "public_records", Category: "表"})
			dataTab := s.currentTab()
			if dataTab.data == nil || dataTab.data.Object().Catalog == "" || dataTab.data.Object().Schema != "PUBLIC" || dataTab.data.Object().ProfileID != p.ID {
				t.Fatal("public H2 missed qualified shared data document")
			}
			wait(tt, func() bool { return !dataTab.data.Busy() })
			if dataTab.data.Error() != "" || dataTab.data.Page() == nil || dataTab.data.Page().Rows[0][0].Text != "9007199254740993" {
				t.Fatal("native grid values lost", dataTab.data.Error())
			}
			s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved via public H2"})
			tt.Frame()
			if err := tt.Click("保存数据更改"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !dataTab.data.Busy() })
			if dataTab.data.Error() != "" || dataTab.data.Page().Rows[0][1].Text != "saved via public H2" {
				t.Fatal("native shared grid save failed", dataTab.data.Error())
			}
			if output := os.Getenv("NYADB_H2_SHELL_SCREENSHOTS"); output != "" {
				writeDomainCapture(t, output, profile+"-grid", tt)
			}
			s.openMySQLStructure(services, "PUBLIC", "public_records")
			tt.Frame()
			structure := s.currentTab()
			if structure.object == nil {
				t.Fatal("structure did not open its owning feature document")
			}
			wait(tt, func() bool { return structure.object.Data() != nil })
			if structure.object.Data().Definition == "" {
				t.Fatal("native full structure definition lost")
			}
			s.openQuery()
			sqlTab := s.currentTab()
			if sqlTab.query == nil || sqlTab.query.ProfileID() != p.ID || sqlTab.query.Database() != "PUBLIC" || sqlTab.query.Namespace().Catalog != dataTab.data.Object().Catalog {
				t.Fatal("new SQL document lost its original H2 profile or qualified namespace")
			}
			sql := `SELECT "id","note" FROM "public_records"`
			sqlTab.query.Editor.Buffer.Set(sql)
			sqlTab.query.Editor.Select(0, len(sql))
			tt.Frame()
			wait(tt, func() bool {
				if !sqlTab.query.Loading() && sqlTab.query.Result() == nil && sqlTab.query.Error() == "" {
					if err := tt.Click("运行当前或选中 SQL"); err != nil {
						t.Fatal(err)
					}
				}
				return sqlTab.query.Loading() || sqlTab.query.Result() != nil || sqlTab.query.Error() != ""
			})
			wait(tt, func() bool { return !sqlTab.query.Loading() })
			result := sqlTab.query.Result()
			if result == nil || result.Sets[0].Rows[0][0].Text != "9007199254740993" || result.Sets[0].Rows[0][1].Text != "saved via public H2" || s.sqlCompletionCatalog(p.ID, "PUBLIC").Dialect != "h2" {
				t.Fatal("native H2 SQL result/dialect lost", sqlTab.query.Error())
			}
			if output := os.Getenv("NYADB_H2_SHELL_SCREENSHOTS"); output != "" {
				writeDomainCapture(t, output, profile+"-sql", tt)
			}
			lease.Release()
			s.disconnectMySQLProfile(p.ID)
			tt.Frame()
			if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql || s.runtime.IsConnected(p.ID) {
				t.Fatal("disconnect lost SQL or retained original runtime")
			}
			for _, tab := range s.tabs {
				if tab.profileID() == p.ID && (tab.data != nil || tab.object != nil) {
					t.Fatal("disconnect retained scoped data/object documents")
				}
			}
			s.sessions.Connect(p, services.Invalidate)
			wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
			if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) == generation {
				t.Fatal("explicit file recovery did not create a new published generation")
			}
			if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sqlTab.query.NeedsRecovery() && !sqlTab.query.Loading() })
			if sqlTab.query.Result() != nil || sqlTab.query.Editor.Buffer.String() != sql {
				t.Fatal("recovery replayed SQL or lost original text")
			}
		})
	}
}
