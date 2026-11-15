package connectioneditor

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	h2svc "github.com/nyakang/nyadb/internal/services/h2"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestH2NativeEditorDriverImportPersistenceCredentialsAndIsolatedTesting(t *testing.T) {
	if os.Getenv("NYADB_H2_EDITOR_IT") != "1" {
		t.Skip("requires owned managed H2 editor fixture")
	}
	var f struct{ Archive, SHA256, Directory, PasswordFile string }
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("H2 editor fixture unavailable")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("H2 editor credential unavailable")
	}
	password := strings.TrimSpace(string(secret))
	store, err := driverstore.New(filepath.Join(f.Directory, "editor-store"))
	if err != nil {
		t.Fatal(err)
	}
	product := driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}
	manager := dbruntime.New()
	provider := h2svc.Provider{Store: store, Started: func(pid int) error {
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
			return fmt.Errorf("captured process identity unavailable")
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "agent": agent, "java": args[0]})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}}
	manager.Register(connections.H2, provider)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.connectionTester = manager.Test
	s.SetDriverPackages(DriverPackages{
		List: func(ctx context.Context, kind connections.Kind, dir string) ([]connections.ManagedDriver, error) {
			return store.List(ctx, product)
		},
		Import: func(ctx context.Context, kind connections.Kind, dir, file string) (connections.ManagedDriver, error) {
			return store.Import(ctx, product, file)
		},
	})
	t.Cleanup(s.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 780, 760) }, 780, 760)
	wait := func(t *testing.T, pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(10 * time.Millisecond)
		}
		if pending() {
			t.Fatal("original editor request did not settle")
		}
	}
	wait(t, func() bool { return s.driverWork.results != nil })
	s.SetEngineFilePicker(func(kind EngineFile, row int, version uint64) {
		if kind == H2DriverPackageFile {
			s.SelectEngineFile(kind, row, version, f.Archive)
		}
	})
	if err := tt.Click("导入驱动包"); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool { return s.driverWork.results != nil })
	if s.connectionDraft.H2Options.DriverSHA256 != f.SHA256 || s.driverWork.message != "" {
		t.Fatal("native selected archive was not pinned", s.driverWork.message)
	}
	s.setH2Mode("memory")
	s.connectionDraft.Name, s.connectionDraft.Database = "owned H2 editor", "owned_editor"
	s.h2Numbers[0] = "120"
	s.connectionSecret, s.connectionSavePassword = password, true
	s.Save()
	profiles, err := service.List(ctx)
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native H2 editor save failed", err, s.Error())
	}
	p := profiles[0]
	if p.H2Options.DriverSHA256 != f.SHA256 || p.H2Options.DriverDirectory == "" {
		t.Fatal("saved H2 driver identity missing")
	}
	published, err := manager.ConnectSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := contract.Require[contract.QueryProvider](published)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	if _, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "original_ui"("id" INT PRIMARY KEY); INSERT INTO "original_ui" VALUES(7)`); err != nil {
		t.Fatal(err)
	}
	generation := manager.Generation(p.ID)
	check := func(t *testing.T, success bool) {
		t.Helper()
		if err := tt.Click("测试连接"); err != nil {
			t.Fatal(err)
		}
		wait(t, func() bool { return s.connectionTesting })
		if success && s.Error() != "" || !success && s.Error() == "" {
			t.Fatal("native connection-test result incorrect", s.Error())
		}
		if manager.Generation(p.ID) != generation {
			t.Fatal("isolated test replaced the original published generation")
		}
		q, err := lease.Execute(ctx, "PUBLIC", `SELECT "id" FROM "original_ui"`)
		if err != nil || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 || q.Sets[0].Rows[0][0].Text != "7" {
			t.Fatal("editor test damaged original SQL session", err)
		}
	}
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		t.Run(profile, func(t *testing.T) {
			draft := p
			draft.H2Options.DriverProfile = profile
			s.OpenProfile(draft)
			wait(t, func() bool { return s.driverWork.results != nil })
			if s.connectionSecret != password {
				t.Fatal("original vault secret not restored")
			}
			check(t, true)
		})
	}
	// The explicit create mode tests an actual owned file before saving it;
	// existing-file tests then reject a genuinely wrong native password.
	s.OpenNew(connections.H2, "")
	wait(t, func() bool { return s.driverWork.results != nil })
	list, err := store.List(ctx, product)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	s.selectH2Driver(list[0])
	s.connectionDraft.Name = "owned H2 file"
	s.connectionDraft.H2Options.RequireExisting = false
	s.SelectEngineFile(H2NewFile, -1, s.EngineFileVersion(), filepath.Join(f.Directory, "editor-created.mv.db"))
	s.h2Numbers[0] = "120"
	s.connectionSecret, s.connectionSavePassword = password, true
	check(t, true)
	s.Save()
	profiles, err = service.List(ctx)
	if err != nil || len(profiles) != 2 || s.Error() != "" {
		t.Fatal("native H2 file profile save failed", err, s.Error())
	}
	var fileProfile connections.Profile
	for _, profile := range profiles {
		if profile.Name == "owned H2 file" {
			fileProfile = profile
		}
	}
	fileProfile.H2Options.RequireExisting = true
	s.OpenProfile(fileProfile)
	wait(t, func() bool { return s.driverWork.results != nil })
	check(t, true)
	s.connectionSecret = password + "-wrong"
	check(t, false)
	runtime := manager.Data(p.ID)
	page, err := runtime.Loader(ctx, p.ID, "PUBLIC", "original_ui", 0, 1)
	if err != nil || len(page.Rows) != 1 || !runtime.NativeReceipts || !runtime.Capabilities.EditableTables {
		t.Fatal("native H2 modern data binding unavailable", err)
	}
	if directory := os.Getenv("NYADB_H2_EDITOR_SCREENSHOTS"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		s.OpenProfile(fileProfile)
		wait(t, func() bool { return s.driverWork.results != nil })
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			tt.Frame()
			for _, tab := range []struct{ caption, file string }{{"常规", "general"}, {"高级", "advanced"}, {"备注", "notes"}} {
				if err := tt.Click(locales.Text(tab.caption)); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				labels := []string{}
				if tab.file == "general" {
					labels = []string{"H2 连接方式", "H2 版本", "H2 驱动包", "导入驱动包", "密码"}
				} else if tab.file == "advanced" {
					labels = []string{"连接池等待（毫秒）", "元数据预留连接数", "SQL 会话上限"}
				}
				for _, label := range labels {
					if _, ok := tt.Find(locales.Text(label)); !ok {
						t.Fatal("H2 localized control unavailable", language, label)
					}
				}
				file, err := os.Create(filepath.Join(directory, language+"-"+tab.file+".png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tt.Image())
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal("H2 editor screenshot unavailable")
				}
			}
		}
	}
}
