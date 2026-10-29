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
	"github.com/nyakang/nyadb/internal/ui/navigation"
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
	if err := s.sessions.Manager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Databases, err = s.sessions.Manager.Databases(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.sessions.Current().Databases, "nyadb_struct_a") {
		t.Fatal("fixture schema not visible")
	}
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(p.ID))
	tt := ui.NewTester(s.View, 1280, 800)
	if err := doubleClickDatabase(tt, "数据库：nyadb_struct_a"); err != nil {
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
	livePage := s.currentTab().dataPage
	if len(livePage.ColumnMetadata) != 8 || livePage.ColumnMetadata[2].Type != "varchar(12)" {
		t.Fatalf("real server field metadata not loaded: %+v", livePage.ColumnMetadata)
	}
	_, visible := mysqlVisibleDataColumns(s.currentTab(), livePage)
	if visible[0].Subtitle != "bigint unsigned" || visible[0].HeaderBadge != "PK" ||
		visible[2].Subtitle != "varchar(12)" || visible[3].Subtitle != "decimal(10,2)" {
		t.Fatalf("complete live MySQL types not rendered by native headers: %+v", visible)
	}
	if !tt.HasText("bigint unsigned") || !tt.HasText("主键") {
		t.Fatalf("real field inspector did not show server attributes: %v", tt.Texts())
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
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
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
	if err := tt.Click("复制所选数据预览"); err != nil || tt.Clipboard() != "P2" {
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
	if err := tt.Click("对象"); err != nil {
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
