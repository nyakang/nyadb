package shell

import (
	"context"
	"image/png"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt-in real MySQL 8.4 -> native MyGo Table rendering, isolated localhost only.
func TestMySQLRealDataNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires disposable mysql:8.4 on 127.0.0.1:34306")
	}
	s, _, service, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Data reader fixture", Kind: connections.MySQL, Host: "127.0.0.1",
		Port: 34306, Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	settings, err := connections.ResolveActiveSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.mysqlSessionManager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s.mysqlSessionProfileID = p.ID
	s.mysqlSessionStatus = "已连接"
	s.mysqlSessionDatabases, err = s.mysqlSessionManager.Databases(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.mysqlSessionDatabases, "nyadb_struct_a") {
		t.Fatal("fixture schema not visible")
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("浏览数据库：nyadb_struct_a"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if s.currentTab().dataPage == nil || s.currentTab().dataPage.Table != "child" ||
		len(s.currentTab().dataPage.Columns) != 8 || len(s.currentTab().dataPage.Rows) != 3 ||
		!tt.HasText("中文备注") || !tt.HasText("1.25") {
		t.Fatalf("real rows were not rendered: %+v", s.currentTab().dataPage)
	}
	if path := os.Getenv("NYADB_MYSQL_DATA_CAPTURE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	tab := s.currentTab()
	tab.dataLimit = 2
	if err := tt.Click("刷新表数据：child"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !tab.dataPage.HasMore || len(tab.dataPage.Rows) != 2 {
		t.Fatal("first 2-row page")
	}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if tab.dataPage.HasMore || len(tab.dataPage.Rows) != 1 || tab.dataPage.Rows[0][0].Text != "3" {
		t.Fatalf("true second page missing: %+v", tab.dataPage)
	}
	// Exercise the actual managed-session query with MyGo's native filters,
	// server sort, row inspector and clipboard, not an injected test loader.
	tab.dataFilterColumnDraft = "parent_code"
	tab.dataFilterValueDraft = "P2"
	tab.dataFilterModeDraft = "精确匹配"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if len(tab.dataPage.Rows) != 1 || tab.dataPage.Rows[0][0].Text != "2" || tab.dataOffset != 0 {
		t.Fatalf("real server filter did not return P2: %+v", tab.dataPage)
	}
	tab.dataSort = ui.SortOrder{Column: "price", Descending: true}
	tt.Frame()
	waitMySQLData(t, s, tt)
	if tab.dataSortColumn != "price" || !tab.dataSortDescending ||
		len(tab.dataPage.Rows) != 1 || tab.dataPage.Rows[0][0].Text != "2" {
		t.Fatalf("real server sort did not preserve filter: %+v", tab.dataPage)
	}
	tab.dataSelectedRow = 0
	tab.dataState.Cells = ui.ColumnRange{Cursor: "parent_code", Anchor: "parent_code"}
	tt.Frame()
	if err := tt.Click("复制所选单元格预览"); err != nil || tt.Clipboard() != "P2" {
		t.Fatalf("real selected value not copied: %v %q", err, tt.Clipboard())
	}
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if len(tab.dataPage.Rows) != 2 || tab.dataFilterMode != "" {
		t.Fatal("real filter clearing failed")
	}
	// Apply multiple AND conditions and ordered sort priorities against real MySQL.
	tab.dataFilterColumnDraft = "parent_code"
	tab.dataFilterModeDraft = "精确匹配"
	tab.dataFilterValueDraft = "P2"
	tab.dataFilterExtraDraft = []mysqlDataRuleDraft{
		{Column: "id", Mode: "不等于", Value: "3"},
		{Column: "price", Mode: "不为空"},
	}
	tab.dataSortDraft = []mysqlDataSortDraft{
		{Column: "price", Direction: "降序"},
		{Column: "id", Direction: "升序"},
	}
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if len(tab.dataPage.Rows) != 1 || tab.dataPage.Rows[0][0].Text != "2" ||
		len(tab.dataFilterRules) != 3 || len(tab.dataSortRules) != 2 {
		t.Fatalf("real multi-rule query failed: %+v / %s", tab.dataPage, tab.dataRuleError)
	}
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if len(s.tabs) != 3 || s.tabs[1].kind != tabMySQLData || s.currentTab().kind != tabStructure {
		t.Fatal("data/design tabs not independent")
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 {
		t.Fatal("data tab/cache survived disconnect")
	}
}
