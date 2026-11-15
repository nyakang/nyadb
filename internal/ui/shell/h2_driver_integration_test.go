package shell

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	h2svc "github.com/nyakang/nyadb/internal/services/h2"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestH2NativeAutomaticAndCustomPublicDriverWorkflows(t *testing.T) {
	if os.Getenv("NYADB_H2_DRIVER_SHELL_IT") != "1" {
		t.Skip("requires owned real H2 automatic/custom UI fixture")
	}
	var fixture struct {
		Archive, Directory, PasswordFile, SHA256 string
		DriverJARs                               map[string]string
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("original driver fixture unavailable")
	}
	secret, err := os.ReadFile(fixture.PasswordFile)
	if err != nil {
		t.Fatal("owned credential unavailable")
	}
	password := strings.TrimSpace(string(secret))
	store, err := driverstore.New(filepath.Join(fixture.Directory, "driver-shell-store"))
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
		file, err := os.OpenFile(filepath.Join(fixture.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(record, '\n'))
		return err
	}
	for _, mode := range []string{"auto", "custom"} {
		for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3"} {
			t.Run(mode+"/"+profile, func(t *testing.T) {
				s, _, service, _ := profileTestState(t)
				t.Cleanup(s.CloseMySQLSessions)
				vault := &memoryCredentialVault{secrets: map[string]string{}}
				s.SetCredentialVault(vault)
				// The production H2 provider resolves the saved package directory;
				// only original-process observation is injected for fixture cleanup.
				s.runtime.Register(connections.H2, h2svc.Provider{Started: observe})
				var imports atomic.Int32
				s.SetDriverPackages(connectioneditor.DriverPackages{
					List: func(ctx context.Context, _ connections.Kind, _ string) ([]connections.ManagedDriver, error) {
						return store.List(ctx, product)
					},
					Import: func(ctx context.Context, _ connections.Kind, _, file string) (connections.ManagedDriver, error) {
						return store.Import(ctx, product, file)
					},
					ImportJDBC: func(ctx context.Context, _ connections.Kind, directory string, request connections.JDBCImport) (connections.ManagedDriver, error) {
						imports.Add(1)
						if directory != filepath.Join(fixture.Directory, "driver-shell-store") {
							return connections.ManagedDriver{}, connections.ErrInvalid
						}
						return store.ImportJDBC(ctx, product, request)
					},
				})
				s.SetConnectionWindowHost(func() {}, func() {})
				s.openConnectionDialog()
				ct := ui.NewTester(s.ConnectionWindowView, 920, 800)
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
					deadline := time.Now().Add(4 * time.Minute)
					for !ready() && time.Now().Before(deadline) {
						tt.Frame()
						time.Sleep(10 * time.Millisecond)
					}
					tt.Frame()
					if !ready() {
						t.Fatal("original native UI request did not settle", s.connectionEditor.Error(), s.sessions.Current().Error)
					}
				}
				input(ct, "搜索连接类型", "H2")
				if err := ct.Click("选择 H2"); err != nil {
					t.Fatal(err)
				}
				if err := ct.Click("下一步"); err != nil {
					t.Fatal(err)
				}
				wait(ct, func() bool { return !s.connectionEditor.DriverBusy() })
				picked := false
				jar := ""
				if mode == "custom" {
					source := filepath.Join(fixture.Directory, "shell-imports", profile)
					if err := os.MkdirAll(source, 0700); err != nil {
						t.Fatal(err)
					}
					original, err := os.ReadFile(fixture.DriverJARs[profile])
					if err != nil {
						t.Fatal(err)
					}
					jar = filepath.Join(source, "h2.jar")
					if err := os.WriteFile(jar, original, 0600); err != nil {
						t.Fatal(err)
					}
					if profile == "h2-v2" {
						entry := filepath.Join(source, "entry.jar")
						file, err := os.Create(entry)
						if err != nil {
							t.Fatal(err)
						}
						archive := zip.NewWriter(file)
						manifest, err := archive.Create("META-INF/MANIFEST.MF")
						if err != nil {
							t.Fatal(err)
						}
						manifest.Write([]byte("Manifest-Version: 1.0\r\nClass-Path: h2.jar\r\n\r\n"))
						archive.Close()
						file.Close()
						jar = entry
					}
				}
				s.SetEngineFilePicker(func(kind EngineFile, row int, version uint64) {
					switch kind {
					case H2DriverPackageFile:
						picked = true
						s.SelectEngineFile(kind, row, version, fixture.Archive)
					case H2CustomJARFile:
						s.SelectEngineFile(kind, row, version, jar)
					}
				})
				if err := ct.Click("导入驱动包"); err != nil {
					t.Fatal(err)
				}
				wait(ct, func() bool { return picked && !s.connectionEditor.DriverBusy() })
				if s.connectionEditor.Draft().H2Options.DriverSHA256 != fixture.SHA256 || s.connectionEditor.Draft().H2Options.DriverProfile != "h2-auto" {
					t.Fatal("automatic default or original imported pin lost")
				}
				if mode == "custom" {
					if err := ct.Click("H2 版本"); err != nil {
						t.Fatal(err)
					}
					if err := ct.Click("自定义 JDBC JAR"); err != nil {
						t.Fatal(err)
					}
					wait(ct, func() bool { return !s.connectionEditor.DriverBusy() })
					if err := ct.Click("高级"); err != nil {
						t.Fatal(err)
					}
					input(ct, "H2 JDBC 驱动类", "org.h2.Driver")
					if err := ct.Click("添加 JDBC JAR"); err != nil {
						t.Fatal(err)
					}
					ct.Frame()
					if err := ct.Click("导入自定义 JDBC 驱动"); err != nil {
						t.Fatal(err)
					}
					wait(ct, func() bool { return !s.connectionEditor.DriverBusy() })
					if imports.Load() != 1 || s.connectionEditor.Draft().H2Options.DriverSHA256 == fixture.SHA256 {
						t.Fatal("actual custom import did not pin a complete new package")
					}
					if output := os.Getenv("NYADB_H2_DRIVER_SCREENSHOTS"); output != "" {
						if err := os.MkdirAll(output, 0700); err != nil {
							t.Fatal(err)
						}
						writeDomainCapture(t, output, mode+"-"+profile+"-advanced", ct)
					}
					if err := os.RemoveAll(filepath.Dir(jar)); err != nil {
						t.Fatal(err)
					}
				}
				pin := s.connectionEditor.Draft().H2Options
				database := filepath.Join(fixture.Directory, "shell-"+mode, profile)
				s.connectionEditor.BeginURIFromEditor()
				uri := "jdbc:h2:file:" + filepath.ToSlash(database) + ".mv.db;IFEXISTS=TRUE;USER=sa;PASSWORD=" + url.PathEscape(password)
				if err := s.connectionEditor.ImportURI(uri); err != nil {
					t.Fatal(err)
				}
				if s.connectionEditor.Draft().H2Options.DriverVersion != pin.DriverVersion || s.connectionEditor.Draft().H2Options.DriverSHA256 != pin.DriverSHA256 || s.connectionEditor.Draft().H2Options.DriverProfile != "h2-"+mode {
					t.Fatal("URI changed original auto/custom pin")
				}
				if err := ct.Click("常规"); err != nil {
					t.Fatal(err)
				}
				input(ct, "连接名称", "H2 "+mode+" "+profile)
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
					t.Fatal("actual driver configuration was not saved", err)
				}
				p := profiles[0]
				stored, exists, err := vault.Get(p.ID, "")
				if err != nil || !exists || stored != password || p.H2Options.DriverSHA256 != pin.DriverSHA256 || p.H2Options.DriverDirectory != filepath.Join(fixture.Directory, "driver-shell-store") {
					t.Fatal("original configuration or vault password lost", err)
				}
				var services ui.Services
				tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
				s.selectProfileByName(p.Name)
				tt.Frame()
				if err := tt.Click("连接所选数据库"); err != nil {
					t.Fatal(err)
				}
				wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
				if !s.runtime.IsConnected(p.ID) {
					t.Fatal("original production driver session unavailable", s.sessions.Current().Error)
				}
				generation := s.runtime.Generation(p.ID)
				session, err := s.runtime.Session(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				queries, err := contract.Require[contract.QueryProvider](session)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := queries.ReserveQuery(991)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
				defer cancel()
				id, err := lease.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
				if err != nil {
					t.Fatal(err)
				}
				s.startEditProfile()
				ct.Frame()
				wait(ct, func() bool { return !s.connectionEditor.DriverBusy() })
				if err := ct.Click("测试连接"); err != nil {
					t.Fatal(err)
				}
				wait(ct, func() bool { return !s.connectionEditor.Testing() })
				if s.connectionEditor.Error() != "" {
					t.Fatal("actual original-profile editor test failed", s.connectionEditor.Error())
				}
				after, err := lease.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
				if err != nil || after.Sets[0].Rows[0][0].Text != id.Sets[0].Rows[0][0].Text || s.runtime.Generation(p.ID) != generation {
					t.Fatal("editor test replaced original generation or physical SQL session", err)
				}
				if err := ct.Click("取消"); err != nil {
					t.Fatal(err)
				}
				s.openMySQLDatabase(services, "PUBLIC", true)
				wait(tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["PUBLIC"] != nil })
				s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "PUBLIC", Object: "driver_records", Category: "表"})
				dataTab := s.currentTab()
				wait(tt, func() bool { return dataTab.data != nil && !dataTab.data.Busy() })
				if dataTab.data.Error() != "" || dataTab.data.Page() == nil || dataTab.data.Object().Catalog == "" || dataTab.data.Page().Rows[0][0].Text != "9007199254740993" {
					t.Fatal("qualified native driver grid lost", dataTab.data.Error())
				}
				note := "saved " + mode + " " + profile
				s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: note})
				tt.Frame()
				if err := tt.Click("保存数据更改"); err != nil {
					t.Fatal(err)
				}
				wait(tt, func() bool { return !dataTab.data.Busy() })
				if dataTab.data.Error() != "" || dataTab.data.Page().Rows[0][1].Text != note {
					t.Fatal("native driver grid save failed", dataTab.data.Error())
				}
				s.openMySQLStructure(services, "PUBLIC", "driver_records")
				tt.Frame()
				structure := s.currentTab()
				wait(tt, func() bool { return structure.object != nil && structure.object.Data() != nil })
				if structure.object.Data().Definition == "" {
					t.Fatal("native full structure lost")
				}
				s.openQuery()
				sqlTab := s.currentTab()
				sql := `SELECT "id","note" FROM "driver_records"`
				sqlTab.query.Editor.Buffer.Set(sql)
				sqlTab.query.Editor.Select(0, len(sql))
				tt.Frame()
				wait(tt, func() bool { return !sqlTab.query.Preparing() })
				if err := tt.Click("运行当前或选中 SQL"); err != nil {
					t.Fatal(err)
				}
				wait(tt, func() bool { return !sqlTab.query.Loading() })
				result := sqlTab.query.Result()
				if result == nil || len(result.Sets) != 1 || result.Sets[0].Rows[0][0].Text != "9007199254740993" || result.Sets[0].Rows[0][1].Text != note {
					t.Fatal("native driver SQL lost", sqlTab.query.Error())
				}
				if output := os.Getenv("NYADB_H2_DRIVER_SCREENSHOTS"); output != "" {
					if err := os.MkdirAll(output, 0700); err != nil {
						t.Fatal(err)
					}
					writeDomainCapture(t, output, mode+"-"+profile+"-sql", tt)
				}
				lease.Release()
				s.disconnectMySQLProfile(p.ID)
				tt.Frame()
				if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
					t.Fatal("driver disconnect lost original SQL")
				}
				for _, tab := range s.tabs {
					if tab.profileID() == p.ID && (tab.data != nil || tab.object != nil) {
						t.Fatal("driver disconnect retained scoped documents")
					}
				}
				s.sessions.Connect(p, services.Invalidate)
				wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
				if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) == generation {
					t.Fatal("explicit original driver reconnect failed")
				}
				if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
					t.Fatal(err)
				}
				wait(tt, func() bool {
					return !sqlTab.query.NeedsRecovery() && !sqlTab.query.Preparing() && !sqlTab.query.Loading()
				})
				if sqlTab.query.Result() != nil || sqlTab.query.Error() != "" || sqlTab.query.Editor.Buffer.String() != sql {
					t.Fatal("driver recovery replayed SQL or failed", sqlTab.query.Error())
				}
			})
		}
	}
}
