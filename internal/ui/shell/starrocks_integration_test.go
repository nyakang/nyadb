package shell

import (
	"context"
	"encoding/json"
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

func TestStarRocksNativeShellFixture(t *testing.T) {
	if os.Getenv("NYADB_STARROCKS_IT") != "1" {
		t.Skip("requires explicit disposable native fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_STARROCKS_CONFIG"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Database string
		PasswordFile                 string `json:"password_file"`
		CAFile                       string `json:"ca_file"`
		ServerName                   string `json:"server_name"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil || !strings.HasPrefix(cfg.Endpoint, "127.0.0.1:") || !strings.HasPrefix(cfg.Database, "nyadb_starrocks_") {
		t.Fatal("requires owned loopback StarRocks fixture")
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(cfg.Endpoint, "127.0.0.1:"), 10, 16)
	if err != nil {
		t.Fatal("invalid fixture port")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture credential")
	}
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, _ := connections.Defaults(connections.StarRocks)
	p.Name = "StarRocks shared grid"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = cfg.Username
	p.Database = cfg.Database
	p.TLSMode = "disable"
	if cfg.CAFile != "" {
		p.TLSMode = "verify-full"
		p.StarRocksOptions.CAFile = cfg.CAFile
		p.StarRocksOptions.ServerName = cfg.ServerName
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
		t.Fatal("StarRocks native profile did not connect")
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
	_, err = lease.Execute(context.Background(), p.Database, "CREATE TABLE `"+table+"` (id BIGINT NOT NULL,note VARCHAR(1048576)) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1'); INSERT INTO `"+table+"` VALUES(9007199254740993,'native shared grid')")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, e := provider.ReserveQuery(100)
		if e == nil {
			defer cleanup.Release()
			_, _ = cleanup.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS `"+table+"`")
		}
	}()
	s.submitNavigation(services, navigation.DatabaseNode(profile.ID, p.Database))
	w := s.sessions.Current()
	wait(func() bool { return w.Pending == "" && w.Cache[p.Database] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: profile.ID, Database: p.Database, Object: table, Category: "表"})
	dataTab := s.currentTab()
	if dataTab.kind != tabMySQLData || dataTab.data == nil || dataTab.data.Object().Catalog != "default_catalog" || dataTab.data.Object().Schema != p.Database {
		t.Fatal("StarRocks did not enter the shared qualified grid")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || !tt.HasText("native shared grid") || dataTab.data.Page().Editable == nil || *dataTab.data.Page().Editable {
		t.Fatalf("native readonly grid failed: %s", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_STARROCKS_VISUAL_DIR"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "starrocks-live-grid", tt)
	}
	before := len(dataTab.data.Page().Rows)
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "must not save"})
	tt.Frame()
	if len(dataTab.data.Page().Rows) != before || dataTab.data.HasDraft() {
		t.Fatal("read-only StarRocks accepted edits")
	}
	s.openMySQLStructure(services, p.Database, table)
	tt.Frame()
	if s.currentTab().kind != tabStructure {
		t.Fatal("structure routed to relational designer")
	}
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT id,note FROM `" + table + "`; EXPLAIN SELECT 1"
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
	if output := os.Getenv("NYADB_STARROCKS_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "starrocks-live-sql", tt)
	}
	s.disconnectMySQLProfile(profile.ID)
	tt.Frame()
	if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql || s.runtime.IsConnected(profile.ID) {
		t.Fatal("disconnect discarded SQL or implicitly reconnected")
	}
}
