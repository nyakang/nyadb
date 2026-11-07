package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestTrinoNativeShellFixture(t *testing.T) {
	if os.Getenv("NYADB_TRINO_IT") != "1" {
		t.Skip("requires explicit disposable native fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TRINO_CONFIG"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Catalog, Schema string
		PasswordFile                        string `json:"password_file"`
		CAFile                              string `json:"ca_file"`
		ServerName                          string `json:"server_name"`
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
	p, _ := connections.Defaults(connections.Trino)
	p.Name = "Trino shared grid"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = cfg.Username
	p.Database = cfg.Catalog
	p.TrinoOptions.DefaultSchema = cfg.Schema
	label := `"` + p.Database + `"."` + cfg.Schema + `"`
	p.TLSMode = "disable"
	if cfg.CAFile != "" {
		p.TLSMode = "verify-full"
		p.TrinoOptions.CAFile = cfg.CAFile
		p.TrinoOptions.ServerName = cfg.ServerName
	}
	profile, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(profile.Name)
	s.sessions.Current().Password = string(secret)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ready() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if !ready() {
			t.Fatal("native UI request did not settle")
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
	_, err = lease.Execute(context.Background(), label, "CREATE TABLE "+table+" (id BIGINT,note VARCHAR); INSERT INTO "+table+" VALUES(9007199254740993,'native shared grid')")
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
	if dataTab.data.Database() != label {
		t.Fatal("data document discarded its qualified namespace label")
	}
	var exported bytes.Buffer
	s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error { return write(&exported) })
	s.dataWorkspace.ExportJSONL(services, dataTab.data)
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Error() != "" || !strings.Contains(exported.String(), "9007199254740993") {
		t.Fatal("shared grid JSONL export failed", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_TRINO_VISUAL_DIR"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "trino-live-grid", tt)
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
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT id,note FROM " + table + "; SELECT CAST(NULL AS DECIMAL(38,18)) WHERE FALSE"
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if err = tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, sqlTab)
	r := sqlTab.query.Result()
	if r == nil || len(r.Sets) != 2 || r.Sets[0].Rows[0][0].Text != "9007199254740993" {
		t.Fatalf("native SQL result failed: %s (%+v)", sqlTab.query.Error(), r)
	}
	if output := os.Getenv("NYADB_TRINO_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "trino-live-sql", tt)
	}
	s.disconnectMySQLProfile(profile.ID)
	tt.Frame()
	if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql || s.runtime.IsConnected(profile.ID) {
		t.Fatal("disconnect discarded SQL or implicitly reconnected")
	}
	s.sessions.Current().Password = string(secret)
	s.startMySQLSession(services)
	wait(func() bool { return s.sessions.StatusFor(profile.ID) != "连接中" })
	if !s.runtime.IsConnected(profile.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("common session connection menu failed or silently recovered SQL")
	}
}
