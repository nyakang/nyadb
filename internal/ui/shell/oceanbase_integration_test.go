package shell

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestOceanBaseSessionAndSharedGridDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_OCEANBASE_IT") != "1" {
		t.Skip("requires disposable OceanBase fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_OCEANBASE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.OceanBase)
	p.Name, p.Host, p.Port, p.Database, p.Username = "OceanBase live grid", "127.0.0.1", uint16(port), "nyadb_oceanbase_it", "nyadb_ob@test"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_OCEANBASE_CA_FILE")
	p, e = service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = os.Getenv("NYADB_OCEANBASE_PASSWORD")
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
			t.Fatal("OceanBase UI operation did not settle")
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.OceanBase {
		t.Fatalf("OceanBase UI connect: %s %s", w.Status, w.Error)
	}
	s.navigation.Filter.Engine = connections.OceanBase
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.MySQL, Name: p.Name}) {
		t.Fatal("OceanBase filter merged product identities")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, p.Database))
	wait(func() bool { return w.Pending == "" && w.Cache[p.Database] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != "" {
		t.Fatal("OceanBase routed to a different data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("OceanBase native grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by OceanBase grid"})
	tt.Frame()
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by OceanBase grid" {
		t.Fatal("OceanBase grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_OCEANBASE_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "oceanbase-live-grid", tt)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "fixture_sequence", Category: "序列"})
	sequence := s.currentTab()
	if sequence.kind != tabProgram || sequence.object == nil || sequence.object.ProgramData().Sequence == nil || sequence.object.ProgramData().Sequence.DefinitionFromServer {
		t.Fatal("sequence did not open a reconstructed read-only document")
	}
	tt.Frame()
	if !tt.HasText("序列定义（根据服务器目录参数重建）") {
		t.Fatal("reconstructed definition mislabeled")
	}
	if output := os.Getenv("NYADB_OCEANBASE_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "oceanbase-live-sequence", tt)
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
	if result == nil || result.Rows[0][0] != "saved by OceanBase grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("OceanBase SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, p.Database).Dialect != "mysql" || s.sqlNamespace(p.ID, p.Database).Catalog != p.Database {
		t.Fatal("OceanBase lost MySQL dialect or namespace")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("OceanBase disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect left scoped data document")
		}
	}
}
