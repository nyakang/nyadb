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

func TestOpenTenBaseSessionSharedGridAndDisconnectFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENTENBASE_IT") != "1" {
		t.Skip("requires owned disposable OpenTenBase")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENTENBASE_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("explicit fixture port required")
	}
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, _ := connections.Defaults(connections.OpenTenBase)
	p.Name, p.Host, p.Port, p.Database, p.Username, p.TLSMode = "OpenTenBase shared grid", "127.0.0.1", uint16(port), "postgres", "opentenbase", "disable"
	p, err = service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if !done() {
			t.Fatal("OpenTenBase UI request did not settle", s.sessions.Current().Error)
		}
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.OpenTenBase {
		t.Fatal("public OpenTenBase provider", w.Error)
	}
	raw, err := s.runtime.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	query, err := contract.Require[contract.QueryProvider](raw)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := query.ReserveQuery(917)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	schema := "nyadb_otb_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ctx := context.Background()
	// The nonce consists only of ASCII identifier characters.
	_, err = lease.Execute(ctx, "public", "CREATE SCHEMA "+schema+"; CREATE TABLE "+schema+`.records (id integer PRIMARY KEY,note text,amount numeric(40,20)) DISTRIBUTE BY SHARD (id); INSERT INTO `+schema+`.records VALUES (1,'before',9007199254740993.12345678901234567890),(2,NULL,NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_, err := lease.Execute(ctx, "public", "DROP SCHEMA "+schema+" CASCADE")
			if err != nil {
				t.Error(err)
			}
		}
	}()
	s.sessions.RefreshDatabases(services.Invalidate)
	wait(func() bool { return !s.sessions.Current().Refreshing() })
	s.navigation.Filter.Engine = connections.OpenTenBase
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.PostgreSQL, Name: p.Name}) {
		t.Fatal("filter merged concrete product with protocol family")
	}
	s.openMySQLDatabase(services, schema, true)
	wait(func() bool { return w.Pending == "" && w.Cache[schema] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != schema {
		t.Fatal("OpenTenBase routed outside the qualified shared data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("native shared grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by OpenTenBase grid"})
	tt.Frame()
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by OpenTenBase grid" || dataTab.data.Error() != "" {
		t.Fatal("native grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_OPENTENBASE_VISUAL_DIR"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "opentenbase-live-grid", tt)
	}
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT note,amount FROM records WHERE id=1"
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if err = tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, sqlTab)
	result := sqlTab.query.Result()
	if result == nil || result.Rows[0][0] != "saved by OpenTenBase grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("SQL typed result", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, schema).Dialect != "postgres" || s.sqlNamespace(p.ID, schema).Catalog != p.Database {
		t.Fatal("native dialect or fixed catalog identity lost")
	}
	if _, err = lease.Execute(ctx, "public", "DROP SCHEMA "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("disconnect lost original SQL or retained physical session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect retained scoped data document")
		}
	}
}
