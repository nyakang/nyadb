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

func TestMariaDBSessionAndSharedGridDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_MARIADB_IT") != "1" {
		t.Skip("requires disposable MariaDB fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MARIADB_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.MariaDB)
	p.Name, p.Host, p.Port, p.Database, p.Username = "MariaDB live grid", "127.0.0.1", uint16(port), "nyadb_maria_it", "nyadb_maria"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_MARIADB_CA_FILE")
	p, e = service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
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
			t.Fatal("MariaDB UI operation did not settle")
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.MariaDB {
		t.Fatalf("MariaDB UI connect: %s %s", w.Status, w.Error)
	}
	s.navigation.Filter.Engine = connections.MariaDB
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.MySQL, Name: p.Name}) {
		t.Fatal("MariaDB filter merged product identities")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, p.Database))
	wait(func() bool { return w.Pending == "" && w.Cache[p.Database] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != "" {
		t.Fatal("MariaDB routed to a different data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("MariaDB native grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by MariaDB grid"})
	tt.Frame()
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by MariaDB grid" {
		t.Fatal("MariaDB grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_MARIADB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "mariadb-live-grid", tt)
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
	if result == nil || result.Rows[0][0] != "saved by MariaDB grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("MariaDB SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, p.Database).Dialect != "mysql" || s.sqlNamespace(p.ID, p.Database).Catalog != p.Database {
		t.Fatal("MariaDB lost MySQL dialect or namespace")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("MariaDB disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect left scoped data document")
		}
	}
}
