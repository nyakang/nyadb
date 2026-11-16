package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/trino"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
)

func TestTrinoManagedNativePublicChooserQualifiedGridSQLAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_AGENT_TRINO_SHELL_IT") != "1" {
		t.Skip("requires explicit disposable native fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_TRINO_FIXTURE_FILE"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Catalog, Schema, Archive, SHA256, Directory string
		DriverVersion                                                   string `json:"driver_version"`
		PasswordFile                                                    string `json:"password_file"`
		CAFile                                                          string `json:"ca_file"`
		ServerName                                                      string `json:"server_name"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil || !strings.HasPrefix(cfg.Endpoint, "https://127.0.0.1:") && !strings.HasPrefix(cfg.Endpoint, "http://127.0.0.1:") {
		t.Fatal("requires owned loopback Trino fixture")
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	if err != nil {
		t.Fatal("invalid fixture port")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture credential")
	}
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	store, err := driverstore.New(filepath.Join(cfg.Directory, "shell-store"))
	if err != nil {
		t.Fatal(err)
	}
	product := driverstore.Product{Key: "trino", Runtime: "java", MainClass: "com.dbx.agent.trino.TrinoAgent"}
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
		file, err := os.OpenFile(filepath.Join(cfg.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(record, '\n'))
		return err
	}
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
	// Attach observation to the original request; the builtin provider remains unchanged.
	s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{
		Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
			return s.runtime.ConnectSession(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
		},
		Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected,
	})
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	ct := ui.NewTester(s.ConnectionWindowView, 920, 780)
	input := func(label, value string) {
		t.Helper()
		if err := ct.Click(label); err != nil {
			t.Fatal(err)
		}
		ct.Command("selectAll")
		ct.Type(value)
	}
	waitEditor := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for !ready() && time.Now().Before(deadline) {
			ct.Frame()
			time.Sleep(5 * time.Millisecond)
		}
		ct.Frame()
		if !ready() {
			t.Fatal("original editor request did not settle", s.connectionEditor.Error())
		}
	}
	input("搜索连接类型", "Trino")
	if err := ct.Click("选择 Trino"); err != nil {
		t.Fatal(err)
	}
	if err := ct.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Trino {
		t.Fatal("public chooser did not open Trino editor")
	}
	if err := ct.Click("Trino 连接方式"); err != nil {
		t.Fatal(err)
	}
	if err := ct.Click("受管 JDBC"); err != nil {
		t.Fatal(err)
	}
	waitEditor(func() bool { return !s.connectionEditor.DriverBusy() })
	s.SetEngineFilePicker(func(kind EngineFile, row int, version uint64) {
		if kind == TrinoDriverPackageFile {
			s.SelectEngineFile(kind, row, version, cfg.Archive)
		}
	})
	if err := ct.Click("导入驱动包"); err != nil {
		t.Fatal(err)
	}
	waitEditor(func() bool { return !s.connectionEditor.DriverBusy() })
	pin := s.connectionEditor.Draft().TrinoOptions
	if pin.Mode != "jdbc" || pin.DriverVersion != cfg.DriverVersion || pin.DriverSHA256 != cfg.SHA256 {
		t.Fatal("public import lost original driver pin")
	}
	s.connectionEditor.BeginURIFromEditor()
	opts := url.Values{}
	opts.Set("sslmode", "disable")
	if cfg.CAFile != "" {
		opts.Set("sslmode", "verify-full")
		opts.Set("ca_file", cfg.CAFile)
		if cfg.ServerName != "" {
			opts.Set("server_name", cfg.ServerName)
		}
	}
	uri := &url.URL{Scheme: "trino", Host: endpoint.Host, Path: "/memory/default", RawQuery: opts.Encode(), User: url.UserPassword(cfg.Username, string(secret))}
	if err := s.connectionEditor.ImportURI(uri.String()); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().TrinoOptions.DriverSHA256 != pin.DriverSHA256 || s.connectionEditor.Draft().TrinoOptions.Mode != "jdbc" {
		t.Fatal("URI replaced original managed pin")
	}
	input("连接名称", "Trino managed public")
	if err := ct.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if err := ct.Click("高级"); err != nil {
		t.Fatal(err)
	}
	input("连接超时（秒）", "120")
	if err := ct.Click("确定"); err != nil {
		t.Fatal(err)
	}
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 {
		t.Fatal("public profile save failed", err, s.connectionEditor.Error())
	}
	profile := profiles[0]
	if profile.Port != uint16(port) || profile.TrinoOptions.DriverDirectory != pin.DriverDirectory {
		t.Fatal("saved endpoint or original package directory lost")
	}
	stored, found, err := vault.Get(profile.ID, "")
	if err != nil || string(secret) != "" && (!found || stored != string(secret)) {
		t.Fatal("original vault credential lost")
	}
	p := profile
	cfg.Catalog, cfg.Schema = "memory", "default"
	label := `"memory"."default"`
	s.selectProfileByName(profile.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for !ready() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if !ready() {
			t.Fatal("native UI request did not settle", s.sessions.Current().Error)
		}
	}
	wait(func() bool { return s.sessions.StatusFor(profile.ID) != "连接中" })
	if s.sessions.StatusFor(profile.ID) != "已连接" || !s.runtime.IsConnected(profile.ID) {
		t.Fatal("Trino native profile did not connect")
	}
	session, _ := s.runtime.Session(profile.ID)
	provider, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	table := "nyadb_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_, err = lease.Execute(context.Background(), label, "CREATE TABLE "+table+" (id BIGINT,note VARCHAR,payload VARBINARY,items ARRAY(BIGINT)); INSERT INTO "+table+" VALUES(9007199254740993,'native shared grid',X'00ff',ARRAY[9007199254740993])")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, e := s.runtime.Session(profile.ID)
		if e != nil {
			return
		}
		bound, e := contract.Require[contract.QueryProvider](current)
		if e != nil {
			return
		}
		cleanup, e := bound.ReserveQuery(100)
		if e == nil {
			defer cleanup.Release()
			_, _ = cleanup.Execute(context.Background(), label, "DROP TABLE IF EXISTS "+table)
		}
	}()
	s.submitNavigation(services, navigation.DatabaseNode(profile.ID, label))
	w := s.sessions.Current()
	wait(func() bool { return w.Pending == "" && w.Cache[label] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: profile.ID, Database: label, Object: table, Category: "表"})
	dataTab := s.currentTab()
	if dataTab.kind != tabMySQLData || dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != cfg.Schema {
		t.Fatal("Trino did not enter the shared qualified grid")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || !tt.HasText("native shared grid") || dataTab.data.Page().Editable == nil || *dataTab.data.Page().Editable {
		t.Fatalf("native readonly grid failed: %s", dataTab.data.Error())
	}
	row := dataTab.data.Page().Rows[0]
	if row[0].Text != "9007199254740993" || !row[2].Truncated || !row[3].Truncated {
		t.Fatal("public grid lost exact scalar or fabricated complete JDBC values")
	}
	if dataTab.data.Database() != label {
		t.Fatal("data document discarded its qualified namespace label")
	}
	var exported bytes.Buffer
	exportCompletion := make(chan error, 1)
	s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error {
		err := write(&exported)
		exportCompletion <- err
		return err
	})
	s.dataWorkspace.ExportJSONL(services, dataTab.data)
	wait(func() bool { return !dataTab.data.Busy() })
	select {
	case err := <-exportCompletion:
		if !errors.Is(err, trino.ErrIncompleteJDBC) || exported.Len() != 0 || !tt.HasText(trino.ErrIncompleteJDBC.Error()) {
			t.Fatal("incomplete JDBC export was not rejected and shown to the user", err, exported.Len())
		}
	default:
		t.Fatal("public JSONL export did not execute its original request")
	}
	if output := os.Getenv("NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "trino-managed-grid", tt)
	}
	before := len(dataTab.data.Page().Rows)
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "must not save"})
	tt.Frame()
	if len(dataTab.data.Page().Rows) != before || dataTab.data.HasDraft() {
		t.Fatal("read-only Trino accepted edits")
	}
	s.openMySQLStructure(services, label, table)
	tt.Frame()
	if s.currentTab().kind != tabStructure {
		t.Fatal("structure routed to relational designer")
	}
	if s.currentTab().object.Database() != label {
		t.Fatal("structure discarded qualified namespace label")
	}
	structure := s.currentTab()
	wait(func() bool { return structure.object.Data() != nil })
	if structure.object.Data().Definition == "" {
		t.Fatal("original complete structure definition lost")
	}
	if output := os.Getenv("NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS"); output != "" {
		writeDomainCapture(t, output, "trino-managed-structure", tt)
	}
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT id,note,payload,items FROM " + table + "; SELECT CAST(NULL AS DECIMAL(38,18)) WHERE FALSE"
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if err = tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, sqlTab)
	r := sqlTab.query.Result()
	if r == nil || len(r.Sets) != 2 || r.Sets[0].Rows[0][0].Text != "9007199254740993" || !r.Sets[0].Rows[0][2].Truncated || !r.Sets[0].Rows[0][3].Truncated {
		t.Fatalf("native SQL result failed: %s (%+v)", sqlTab.query.Error(), r)
	}
	if output := os.Getenv("NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS"); output != "" {
		writeDomainCapture(t, output, "trino-managed-sql", tt)
	}
	planSource := "SELECT r.name,COUNT(*) FROM tpch.tiny.nation n JOIN tpch.tiny.region r ON n.regionkey=r.regionkey GROUP BY r.name ORDER BY COUNT(*) DESC"
	sqlTab.query.Editor.Buffer.Set(planSource)
	sqlTab.query.Editor.Select(0, len(planSource))
	tt.Frame()
	if err = tt.Click("EXPLAIN"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err = tt.Click("执行 EXPLAIN"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, sqlTab)
	if !tt.HasText("Trino estimated plan") || sqlTab.query.Error() != "" || sqlTab.query.Result() == nil {
		t.Fatal("native plan did not enter shared view", sqlTab.query.Error())
	}
	if output := os.Getenv("NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS"); output != "" {
		writeDomainCapture(t, output, "trino-managed-plan", tt)
	}
	if err = tt.Click("原始计划"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	planResult := sqlTab.query.Result()
	if len(planResult.Sets) != 1 || len(planResult.Sets[0].Rows[0][0].Text) <= 4096 || planResult.Sets[0].Rows[0][0].Truncated {
		t.Fatal("original native JSON plan was clipped or replaced")
	}
	if err = tt.Click("查询结果"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if sqlTab.query.Result() != r || r.Sets[0].Rows[0][0].Text != "9007199254740993" {
		t.Fatal("plan navigation lost ordinary multi-results")
	}
	if err = tt.Click("结构计划"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if sqlTab.query.Result() != planResult || !tt.HasText("Trino estimated plan") {
		t.Fatal("structured plan lost original result")
	}
	s.disconnectMySQLProfile(profile.ID)
	tt.Frame()
	if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != planSource || s.runtime.IsConnected(profile.ID) {
		t.Fatal("disconnect discarded SQL or implicitly reconnected")
	}
	s.sessions.Current().Password = string(secret)
	s.startMySQLSession(services)
	wait(func() bool { return s.sessions.StatusFor(profile.ID) != "连接中" })
	if !s.runtime.IsConnected(profile.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != planSource {
		t.Fatal("common session connection menu failed or silently recovered SQL")
	}
	for _, tab := range s.tabs {
		if tab.profileID() == profile.ID && (tab.data != nil || tab.object != nil) {
			t.Fatal("disconnect retained scoped data/object documents")
		}
	}
	if err := tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !sqlTab.query.NeedsRecovery() && !sqlTab.query.Loading() })
	if sqlTab.query.Result() != nil || sqlTab.query.Editor.Buffer.String() != planSource {
		t.Fatal("recovery replayed original SQL or discarded text")
	}
	if output := os.Getenv("NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS"); output != "" {
		writeDomainCapture(t, output, "trino-managed-recovery", tt)
	}

}
