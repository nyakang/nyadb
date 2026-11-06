package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	ducksvc "github.com/nyakang/nyadb/internal/services/duckdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestDuckDBSessionAndSharedGridFixture(t *testing.T) {
	if os.Getenv("NYADB_DUCKDB_IT") != "1" {
		t.Skip("requires the verified DuckDB native runtime")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.DuckDB)
	p.Name = "DuckDB live grid"
	p.Database = filepath.Join(t.TempDir(), "live.duckdb")
	p.DuckDBOptions.RequireExisting = false
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := ducksvc.OpenSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](fixture)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lease.Execute(context.Background(), "", `CREATE TABLE records(id INTEGER PRIMARY KEY,note VARCHAR,amount DECIMAL(38,20)); INSERT INTO records VALUES(1,'initial',9007199254740993.12345678901234567890),(2,'other',2)`)
	lease.Release()
	fixture.Close()
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("DuckDB UI operation did not settle")
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.DuckDB || w.Catalog == "" {
		t.Fatalf("DuckDB connect: %s %s %s", w.Status, w.Error, w.Catalog)
	}
	s.navigation.Filter.Engine = connections.DuckDB
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.SQLite, Name: p.Name}) {
		t.Fatal("DuckDB filter mixed engine identities")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, "main"))
	wait(func() bool { return w.Pending == "" && w.Cache["main"] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "main", Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != w.Catalog || dataTab.data.Object().Schema != "main" {
		t.Fatal("DuckDB did not open the shared qualified data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("DuckDB grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by DuckDB grid"})
	tt.Frame()
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by DuckDB grid" {
		t.Fatal("DuckDB grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_DUCKDB_VISUAL_DIR"); output != "" {
		if err = os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "duckdb-live-grid", tt)
	}
	s.openQuery()
	sqlTab := s.currentTab()
	source := "SELECT note,amount FROM main.records WHERE id=1"
	sqlTab.query.Editor.Buffer.Set(source)
	sqlTab.query.Editor.Select(0, len(source))
	tt.Frame()
	if err = tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, sqlTab)
	result := sqlTab.query.Result()
	if result == nil || len(result.Rows) != 1 || result.Rows[0][0] != "saved by DuckDB grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("DuckDB SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, "main").Dialect != "duckdb" || s.sqlNamespace(p.ID, "main").Catalog != w.Catalog {
		t.Fatal("DuckDB dialect or qualified namespace was lost")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != source {
		t.Fatal("DuckDB disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect retained a scoped data document")
		}
	}
}
