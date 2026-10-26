package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func alignedDataTestTab() *tab {
	return &tab{
		kind: tabMySQLData, dataTable: "records", dataLimit: 100,
		dataColumns: []ui.TableColumn{{ID: "id", Title: "id", Width: 100}, {ID: "label", Title: "label", Width: 160}},
		dataPage: &connections.MySQLDataPage{
			Columns: []string{"id", "label"}, StableOrder: true,
			Rows: [][]connections.MySQLDataCell{{{Text: "1"}, {Text: "safe-value"}}},
		},
	}
}

func TestExpandedDataRulesKeepGridAndPagingReachable(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataRulesExpanded = true
	for range connections.MySQLDataMaxFilterRules - 1 {
		doc.dataFilterExtraDraft = append(doc.dataFilterExtraDraft, mysqlDataRuleDraft{Column: "label", Mode: "包含"})
	}
	for range connections.MySQLDataMaxSortRules {
		doc.dataSortDraft = append(doc.dataSortDraft, mysqlDataSortDraft{Column: "label", Direction: "升序"})
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataWorkspace(c, doc)
	}, 384, 410)
	rules, ok := tt.Find("筛选规则")
	if !ok || rules.H > 124 {
		t.Fatalf("rules consumed the available grid space: %+v", rules)
	}
	for _, name := range []string{"应用筛选", "下一页数据", "上一页数据", "safe-value"} {
		r, ok := tt.Find(name)
		if !ok || r.X < 0 || r.X+r.W > 384 || r.Y < 0 || r.Y+r.H > 410 {
			t.Fatalf("%s became unreachable with expanded rules: %+v", name, r)
		}
	}
	// A server failure must not let a long message push the retry control or
	// paging outside the viewport, and clearing/revising rules remains possible.
	doc.dataPage = nil
	doc.dataErr = strings.Repeat("query failed ", 40)
	tt.Frame()
	for _, name := range []string{"重试读取数据", "应用筛选", "下一页数据"} {
		r, ok := tt.Find(name)
		if !ok || r.X+r.W > 384 || r.Y+r.H > 410 {
			t.Fatalf("failure state hid %s: %+v", name, r)
		}
	}
}

func TestDataRowHeaderKeepsFieldMenusAndCellCopyMapped(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataWorkspace(c, doc)
	}, 540, 320)
	if tt.HasText("#") {
		t.Fatal("row numbers should be optional")
	}
	cell, ok := tt.Find("safe-value")
	if !ok {
		t.Fatal("data cell is missing")
	}
	tt.ClickAt(mysqlDataRowHeaderColumn(doc).Width/2, cell.Y+cell.H/2)
	if !doc.dataState.Cells.All || doc.dataSelectedRow != 0 {
		t.Fatal("narrow row selector did not choose the whole row")
	}
	if err := tt.Click("safe-value"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制所选单元格预览"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "safe-value" {
		t.Fatalf("row header shifted selected cell: %q", tt.Clipboard())
	}
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制字段名称"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "label" {
		t.Fatalf("row header shifted field context menu: %q", tt.Clipboard())
	}
}

func TestStructureTabsAndEmptyHeadersSurviveLoadFailure(t *testing.T) {
	s := New()
	doc := &tab{kind: tabStructure, structureDatabase: "example", structureTable: "records"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlStructureWorkspace(c, doc)
	}, 384, 400)
	before, ok := tt.Find("索引")
	if !ok || !tt.HasText("不是 null") {
		t.Fatal("unloaded structure lost tabs or column headers")
	}
	doc.structureErr = strings.Repeat("metadata unavailable ", 40)
	tt.Frame()
	after, ok := tt.Find("索引")
	if !ok || before.Y != after.Y {
		t.Fatalf("status message moved structure tabs: before=%+v after=%+v", before, after)
	}
	r, ok := tt.Find("重试读取结构")
	if !ok || r.X+r.W > 384 {
		t.Fatalf("long error pushed retry offscreen: %+v", r)
	}
	doc.structure = &connections.MySQLTableStructure{}
	doc.structureErr = ""
	if err := tt.Click("外键"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("被引用的数据库") || !tt.HasText("此表没有外键") {
		t.Fatal("empty foreign-key page lost its schema or empty state")
	}
}

func TestInspectorLargeFieldsKeepCopyAndChunkActionsVisible(t *testing.T) {
	s := New()
	doc := mysqlInspectorSampleTab()
	doc.dataSelectedRow = 0
	doc.dataState.Cells = ui.ColumnRange{Cursor: "label", Anchor: "label"}
	doc.dataPage.RowRefs = []*connections.MySQLRowRef{{Database: "test_schema", Table: "entries"}}
	doc.dataPage.Rows[0][1].Text = strings.Repeat("large preview ", 300)
	doc.dataPage.ColumnMetadata[1].Comment = strings.Repeat("long metadata ", 200)
	doc.dataChunkField, doc.dataChunkRow = "label", 0
	doc.dataChunkNext, doc.dataChunkTotal = 1024, 8192
	doc.dataChunkText = "0x" + strings.Repeat("ab", 1024)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataInspector(c, doc)
	}, 205, 400)
	header, ok := tt.Find("数据信息切换栏")
	if !ok || header.Y != 0 || header.H != 42 {
		t.Fatalf("inspector header no longer occupies the fixed top region: %+v", header)
	}
	actionsTop := header.Y + header.H
	actionsBottom := actionsTop + theme.CommandBarHeight
	for _, label := range []string{"复制单元格预览", "读取所选字段下一段", "复制此片段"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 205 || r.Y < actionsTop || r.Y+r.H > actionsBottom {
			t.Fatalf("large values displaced inspector action %s: %+v", label, r)
		}
	}
	if err := tt.Click("复制此片段"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != doc.dataChunkText {
		t.Fatal("bounded display altered the copied fragment")
	}
}
