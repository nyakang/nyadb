package connectioneditor

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/trino"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestTrinoManagedNativeEditorImportPersistenceCredentialsAndRevocation(t *testing.T) {
	if os.Getenv("NYADB_AGENT_TRINO_EDITOR_IT") != "1" {
		t.Skip("requires owned native Trino editor fixture")
	}
	var f struct {
		Archive, SHA256, Directory, Endpoint, Username string
		DriverVersion                                  string `json:"driver_version"`
		PasswordFile                                   string `json:"password_file"`
		CAFile                                         string `json:"ca_file"`
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_TRINO_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("private fixture unavailable")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	store, err := driverstore.New(filepath.Join(f.Directory, "editor-store"))
	if err != nil {
		t.Fatal(err)
	}
	product := driverstore.Product{Key: "trino", Runtime: "java", MainClass: "com.dbx.agent.trino.TrinoAgent"}
	manager := dbruntime.New()
	provider := trino.Provider{Store: store, Started: func(pid int) error {
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
	manager.Register(connections.Trino, provider)
	t.Cleanup(func() { manager.Close() })
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.Close)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.connectionTester = manager.Test
	s.SetDriverPackages(DriverPackages{List: func(ctx context.Context, kind connections.Kind, dir string) ([]connections.ManagedDriver, error) {
		return store.List(ctx, product)
	}, Import: func(ctx context.Context, kind connections.Kind, dir, file string) (connections.ManagedDriver, error) {
		return store.Import(ctx, product, file)
	}})
	s.OpenNew(connections.Trino, "")
	s.setTrinoMode("jdbc")
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 920, 780) }, 920, 780)
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(5 * time.Millisecond)
		}
		tt.Frame()
		if pending() {
			t.Fatal("original editor request did not settle")
		}
	}
	wait(s.DriverBusy)
	s.SetEngineFilePicker(func(kind EngineFile, row int, version uint64) {
		if kind == TrinoDriverPackageFile {
			s.SelectEngineFile(kind, row, version, f.Archive)
		}
	})
	if err := tt.Click("导入驱动包"); err != nil {
		t.Fatal(err)
	}
	wait(s.DriverBusy)
	if s.connectionDraft.TrinoOptions.DriverSHA256 != f.SHA256 || s.driverWork.message != "" {
		t.Fatal("original package was not selected", s.driverWork.message)
	}
	u, _ := url.Parse(f.Endpoint)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	s.connectionDraft.Name, s.connectionDraft.Host, s.connectionDraft.Port, s.connectionPort, s.connectionDraft.Username, s.connectionDraft.Database = "Trino managed editor", u.Hostname(), uint16(port), u.Port(), f.Username, "memory"
	s.connectionDraft.TrinoOptions.DefaultSchema, s.connectionDraft.TrinoOptions.CAFile = "default", f.CAFile
	s.trinoNumbers[0] = "120"
	if u.Scheme == "http" {
		s.connectionDraft.TLSMode = "disable"
	}
	s.connectionSecret, s.connectionSavePassword = string(secret), true
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native editor save failed", err, s.Error())
	}
	p := profiles[0]
	if p.TrinoOptions.Mode != "jdbc" || p.TrinoOptions.DriverSHA256 != f.SHA256 || p.TrinoOptions.DriverVersion != f.DriverVersion {
		t.Fatal("saved original pin was lost")
	}
	s.OpenProfile(p)
	wait(s.DriverBusy)
	if s.connectionSecret != string(secret) {
		t.Fatal("original vault password was not restored")
	}
	session, err := manager.ConnectSession(context.Background(), p, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	queries, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	generation := manager.Generation(p.ID)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.connectionTesting })
	if s.Error() != "" || manager.Generation(p.ID) != generation {
		t.Fatal("isolated native test changed original publication", s.Error())
	}
	q, err := lease.Execute(context.Background(), "", "SELECT 9007199254740993")
	if err != nil || q.Sets[0].Rows[0][0].Text != "9007199254740993" {
		t.Fatal("test invalidated original physical SQL lease", err)
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI("trino://" + url.PathEscape(f.Username) + "@" + u.Host + "/memory/default?sslmode=" + p.TLSMode); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.TrinoOptions.DriverSHA256 != p.TrinoOptions.DriverSHA256 || s.connectionDraft.TrinoOptions.Mode != "jdbc" {
		t.Fatal("URI silently replaced original driver choice")
	}
	s.OpenProfile(p)
	wait(s.DriverBusy)
	if output := os.Getenv("NYADB_TRINO_EDITOR_SCREENSHOTS"); output != "" {
		if err := os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		controls := map[string]map[string]ui.Rect{}
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range []struct {
				name  string
				width float32
			}{{"wide", 920}, {"narrow", 720}} {
				capture := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, size.width, 780) }, int(size.width), 780)
				for _, tab := range []struct{ caption, name string }{{"常规", "general"}, {"高级", "advanced"}, {"SSL", "ssl"}} {
					capture.Frame()
					if err := capture.Click(locales.Text(tab.caption)); err != nil {
						t.Fatal(err)
					}
					capture.Frame()
					labels := map[string][]string{
						"general":  {"Trino 连接方式", "Trino 驱动包", "导入驱动包", "刷新驱动包", "密码"},
						"advanced": {"连接超时（秒）", "查询超时（秒）", "连接池等待（毫秒）", "最大连接数", "元数据预留连接数", "SQL 会话上限"},
						"ssl":      {"Trino TLS 模式", "Trino CA 证书", "选择 Trino CA 证书", "证书服务器名"},
					}[tab.name]
					name := language + "-" + size.name + "-" + tab.name + ".png"
					controls[name] = map[string]ui.Rect{}
					for _, label := range labels {
						box, ok := capture.Find(locales.Text(label))
						if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > size.width+1 || box.Y+box.H > 781 || math.IsNaN(float64(box.X+box.Y+box.W+box.H)) {
							t.Fatal("localized Trino control clipped or missing", language, size.name, tab.name, label, box)
						}
						controls[name][label] = box
					}
					file, err := os.Create(filepath.Join(output, language+"-"+size.name+"-"+tab.name+".png"))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(file, capture.Image())
					closeErr := file.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err)
					}
				}
			}
		}
		manifest, err := json.MarshalIndent(controls, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "controls.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
