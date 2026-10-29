package dataworkspace

import (
	"context"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Opt-in real MySQL 8.4 -> native MyGo Table rendering, isolated localhost only.
func TestMySQLRealDataNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires disposable mysql:8.4 on 127.0.0.1:34306")
	}
	manager := mysqlsvc.NewMySQLSessionManager()
	t.Cleanup(func() { _ = manager.Close() })
	settings := connections.ActiveSettings{ProfileID: 12, Host: "127.0.0.1", Port: 34306, Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Connected: true} }, Runtime: func() Runtime { return MySQLRuntime(manager) }, Inspector: InspectorDependencies{Session: func() Session { return Session{ProfileID: 12, Connected: true} }}})
	doc := s.Open(1, 12, "nyadb_struct_a", "child")
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		theme.Apply(c)
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() { s.View(c, doc) })
			ui.Box(c).Width(250).Children(func() { s.InspectorView(c, doc) })
		})
	}, 1280, 800)
	s.Load(ui.Services{}, doc, false)
	waitMySQLData(t, s, doc, tt)
	if doc.page == nil || doc.page.Table != "child" ||
		len(doc.page.Columns) != 8 || len(doc.page.Rows) != 3 ||
		!tt.HasText("中文备注") || !tt.HasText("1.25") {
		t.Fatalf("real rows were not rendered: %+v", doc.page)
	}
	livePage := doc.page
	if len(livePage.ColumnMetadata) != 8 || livePage.ColumnMetadata[2].Type != "varchar(12)" {
		t.Fatalf("real server field metadata not loaded: %+v", livePage.ColumnMetadata)
	}
	_, visible := mysqlVisibleDataColumns(doc, livePage)
	if visible[0].Subtitle != "bigint unsigned" || visible[0].HeaderBadge != "PK" ||
		visible[2].Subtitle != "varchar(12)" || visible[3].Subtitle != "decimal(10,2)" {
		t.Fatalf("complete live MySQL types not rendered by native headers: %+v", visible)
	}
	s.ShowField(doc)
	tt.Frame()
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
	tab := doc
	tab.limit = 2
	if err := tt.Click("刷新表数据：child"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if !tab.page.HasMore || len(tab.page.Rows) != 2 {
		t.Fatal("first 2-row page")
	}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if tab.page.HasMore || len(tab.page.Rows) != 1 || tab.page.Rows[0][0].Text != "3" {
		t.Fatalf("true second page missing: %+v", tab.page)
	}
	// Exercise the actual managed-session query with MyGo's native filters,
	// server sort, row inspector and clipboard, not an injected test loader.
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	tab.filterColumnDraft = "parent_code"
	tab.filterValueDraft = "P2"
	tab.filterModeDraft = "精确匹配"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if len(tab.page.Rows) != 1 || tab.page.Rows[0][0].Text != "2" || tab.offset != 0 {
		t.Fatalf("real server filter did not return P2: %+v", tab.page)
	}
	tab.sort = ui.SortOrder{Column: "price", Descending: true}
	tt.Frame()
	waitMySQLData(t, s, doc, tt)
	if tab.sortColumn != "price" || !tab.sortDescending ||
		len(tab.page.Rows) != 1 || tab.page.Rows[0][0].Text != "2" {
		t.Fatalf("real server sort did not preserve filter: %+v", tab.page)
	}
	tab.selectedRow = 0
	tab.grid.Cells = ui.ColumnRange{Cursor: "parent_code", Anchor: "parent_code"}
	tt.Frame()
	if err := tt.Click("复制所选数据预览"); err != nil || tt.Clipboard() != "P2" {
		t.Fatalf("real selected value not copied: %v %q", err, tt.Clipboard())
	}
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if len(tab.page.Rows) != 2 || tab.filterMode != "" {
		t.Fatal("real filter clearing failed")
	}
	// Apply multiple AND conditions and ordered sort priorities against real MySQL.
	tab.filterColumnDraft = "parent_code"
	tab.filterModeDraft = "精确匹配"
	tab.filterValueDraft = "P2"
	tab.filterExtraDraft = []mysqlDataRuleDraft{
		{Column: "id", Mode: "不等于", Value: "3"},
		{Column: "price", Mode: "不为空"},
	}
	tab.sortDraft = []mysqlDataSortDraft{
		{Column: "price", Direction: "降序"},
		{Column: "id", Direction: "升序"},
	}
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if len(tab.page.Rows) != 1 || tab.page.Rows[0][0].Text != "2" ||
		len(tab.filterRules) != 3 || len(tab.sortRules) != 2 {
		t.Fatalf("real multi-rule query failed: %+v / %s", tab.page, tab.ruleError)
	}
	s.Close(doc)
	tt.Frame()
	if len(s.documents) != 0 || doc.page != nil {
		t.Fatal("closed data document retained cache")
	}
}
