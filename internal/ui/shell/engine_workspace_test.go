package shell

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRealProfileSchemaDataSQLDisconnectKeepsText(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	historyDB, e := localdb.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.CloseMySQLSessions(); historyDB.Close() })
	historyKey := &assetTestKey{}
	historyStore := queries.NewStore(localdb.NewSQLRepository(historyDB), historyKey)
	s.SetSQLPersistence(historyStore, sqlworkspace.Files{}, nil)
	profile, e := service.Create(context.Background(), connections.Profile{Name: "SQLite user fixture", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "user.sqlite")})
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(profile.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	tt.Frame()
	if e = tt.Click("连接所选数据库"); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.sessions.StatusFor(profile.ID) == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	w := s.sessions.Current()
	if w.Status != "已连接" || len(w.Databases) != 1 || w.Databases[0] != "main" {
		t.Fatalf("SQLite real connect %s %#v", w.Error, w)
	}
	session, e := s.engineSession(profile.ID)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := session.Reserve(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	_, e = lease.Execute(context.Background(), `CREATE TABLE records(id INTEGER PRIMARY KEY,note TEXT); INSERT INTO records VALUES(1,'before');`)
	lease.Close()
	if e != nil {
		t.Fatal(e)
	}
	s.submitNavigation(services, navigation.DatabaseNode(profile.ID, "main"))
	deadline = time.Now().Add(3 * time.Second)
	for (w.Pending != "" || w.Cache["main"] == nil) && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if len(w.Cache["main"]) != 1 || w.Cache["main"][0].Name != "records" {
		t.Fatalf("metadata came from config DB or missing user table %#v", w.Cache)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: profile.ID, Database: "main", Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.kind != tabMySQLData || dataTab.data.Object().Catalog != "main" || dataTab.data.Object().Schema != "main" {
		t.Fatalf("qualified generic document absent %+v", dataTab)
	}
	deadline = time.Now().Add(3 * time.Second)
	for dataTab.data.Busy() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	tt.Frame()
	if dataTab.data.Page() == nil || !tt.HasText("before") {
		t.Fatalf("native real grid missing %s", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "changed in grid"})
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(3 * time.Second)
	for dataTab.data.Busy() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if dataTab.data.Page().Rows[0][1].Text != "changed in grid" {
		t.Fatalf("graphical write not saved %s", dataTab.data.Error())
	}
	s.openMySQLStructure(services, "main", "records")
	if s.currentTab().object == nil || s.currentTab().kind != tabStructure {
		t.Fatal("generic structure entry dispatched to MySQL designer")
	}

	s.openQuery()
	sqlTab := s.currentTab()
	sql := `SELECT NULL AS null_value,'NULL' AS text_value,'' AS empty_value,x'' AS binary_value,note FROM records; SELECT 2;`
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	r := sqlTab.query.Result()
	if r == nil || len(r.Sets) != 2 || r.Sets[0].Rows[0][0].Kind != contract.Null || r.Sets[0].Rows[0][1].Kind != contract.Text || r.Sets[0].Rows[0][3].Kind != contract.Binary {
		t.Fatalf("typed generic SQL absent %+v (%s)", r, sqlTab.query.Error())
	}
	historyDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(historyDeadline) {
		tt.Frame()
		if !s.sqlWorkspace.HistoryPending() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	seen := map[string]bool{}
	for _, entry := range s.sqlWorkspace.HistorySnapshot().Entries {
		if entry.ProfileID != profile.ID || entry.Database != "main" {
			t.Fatal("global activity lost original SQLite scope")
		}
		seen[entry.Source] = true
		if entry.Source == "data_read" || entry.Source == "data_write" {
			if !entry.Summary || entry.Object != "records" || entry.Generation == 0 || strings.Contains(entry.SQL, "changed in grid") || strings.Contains(entry.SQL, "before") {
				t.Fatal("data activity lost summary safety or qualified metadata")
			}
		}
	}
	if !seen["data_read"] || !seen["data_write"] || !seen["query"] {
		t.Fatalf("real SQLite activity did not reach global history: %v", seen)
	}
	restored, err := queries.NewStore(localdb.NewSQLRepository(historyDB), historyKey).LoadHistory(context.Background(), time.Now())
	if err != nil || len(restored.Entries) != len(s.sqlWorkspace.HistorySnapshot().Entries) {
		t.Fatal("global activity did not survive encrypted restart")
	}
	s.disconnectMySQLProfile(profile.ID)
	tt.Frame()
	if s.runtime.IsConnected(profile.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("disconnect discarded SQL or allowed implicit session recovery")
	}
	for _, tab := range s.tabs {
		if tab.data != nil {
			t.Fatal("disconnect left scoped data document")
		}
	}
}
