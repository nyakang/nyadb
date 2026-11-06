package shell

import (
	"context"
	"fmt"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestTiDBSessionAndSharedGridDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_TIDB_IT") != "1" {
		t.Skip("requires disposable TiDB fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_TIDB_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.TiDB)
	p.Name, p.Host, p.Port, p.Database, p.Username = "TiDB live grid", "127.0.0.1", uint16(port), "nyadb_tidb_it", "nyadb_tidb"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_TIDB_CA_FILE")
	p.ID = 705
	seed, e := mysqlsvc.OpenSession(context.Background(), p, os.Getenv("NYADB_TIDB_PASSWORD"), nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { seed.Close() })
	l, e := seed.(contract.QueryProvider).ReserveQuery(55)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(l.Release)
	p.Database = fmt.Sprintf("nyadb_tidb_ui_%d", time.Now().UnixNano())
	_, e = l.Execute(context.Background(), "nyadb_tidb_it", "CREATE DATABASE `"+p.Database+"`")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, e = l.Execute(ctx, "nyadb_tidb_it", "DROP DATABASE IF EXISTS `"+p.Database+"`")
		if e != nil {
			t.Error(e)
		}
	})
	for _, sql := range []string{"CREATE TABLE records(id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20))", "INSERT INTO records VALUES(1,'seed',9007199254740993.12345678901234567890)", "CREATE SEQUENCE ui_sequence START WITH 123"} {
		_, e = l.Execute(context.Background(), p.Database, sql)
		if e != nil {
			t.Fatal(e)
		}
	}

	p, e = service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = os.Getenv("NYADB_TIDB_PASSWORD")
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if e = tt.Click("连接所选数据库"); e != nil {
		t.Fatal(e)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("TiDB UI operation did not settle")
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.TiDB {
		t.Fatalf("TiDB UI connect: %s %s", w.Status, w.Error)
	}
	if s.toolbarActionAvailable("函数") {
		t.Fatal("TiDB toolbar advertised stored functions")
	}
	s.navigation.Filter.Engine = connections.TiDB
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.MySQL, Name: p.Name}) {
		t.Fatal("TiDB filter merged product identities")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, p.Database))
	wait(func() bool { return w.Pending == "" && w.Cache[p.Database] != nil })
	if e = tt.Click("新建表"); e != nil {
		t.Fatal(e)
	}
	if e = tt.Click("新表名称"); e != nil {
		t.Fatal(e)
	}
	tt.Type("ui_designed")
	if e = tt.Click("确认新建表"); e != nil {
		t.Fatal(e)
	}
	design := s.currentTab().object
	if design == nil {
		t.Fatal("TiDB native designer did not open")
	}
	tt.Frame()
	if tt.HasText("触发器") {
		t.Fatal("TiDB native designer advertised triggers")
	}
	tt.Key(primaryModifier(), ui.KeyS)
	if e = tt.Click("确认执行结构 SQL"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !design.DesignBusy() })
	if design.Error() != "" || design.DesignDirty() {
		t.Fatal("TiDB native table create", design.Error())
	}
	if output := os.Getenv("NYADB_TIDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "tidb-live-designer", tt)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != "" {
		t.Fatal("TiDB routed to a different data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("TiDB native grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by TiDB grid"})
	tt.Frame()
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by TiDB grid" {
		t.Fatal("TiDB grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_TIDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "tidb-live-grid", tt)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "ui_sequence", Category: "序列"})
	sequence := s.currentTab()
	if sequence.kind != tabProgram || sequence.object == nil || sequence.object.ProgramData().Sequence == nil || sequence.object.ProgramData().Sequence.Start != 123 {
		t.Fatal("TiDB sequence did not open its complete read-only document")
	}
	tt.Frame()
	if tt.HasText("序列定义（根据服务器目录参数重建）") {
		t.Fatal("TiDB SHOW CREATE definition mislabeled as reconstructed")
	}
	if output := os.Getenv("NYADB_TIDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "tidb-live-sequence", tt)
	}
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT note,amount FROM records WHERE id=1"
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	result := sqlTab.query.Result()
	if result == nil || result.Rows[0][0] != "saved by TiDB grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("TiDB SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, p.Database).Dialect != "mysql" || s.sqlNamespace(p.ID, p.Database).Catalog != p.Database {
		t.Fatal("TiDB lost MySQL dialect or namespace")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("TiDB disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect left scoped data document")
		}
	}
}
