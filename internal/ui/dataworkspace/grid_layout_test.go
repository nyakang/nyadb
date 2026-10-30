package dataworkspace

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func alignedDataTestTab() *Document {
	return &Document{
		table: "records", limit: 100,
		columns: []ui.TableColumn{{ID: "id", Title: "id", Width: 100}, {ID: "label", Title: "label", Width: 160}},
		page: &mysqlsvc.MySQLDataPage{
			Columns: []string{"id", "label"}, StableOrder: true,
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "safe-value"}}},
		},
	}
}

func TestExpandedDataRulesKeepGridAndPagingReachable(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.rulesExpanded = true
	for range mysqlsvc.MySQLDataMaxFilterRules - 1 {
		doc.filterExtraDraft = append(doc.filterExtraDraft, mysqlDataRuleDraft{Column: "label", Mode: "包含"})
	}
	for range mysqlsvc.MySQLDataMaxSortRules {
		doc.sortDraft = append(doc.sortDraft, mysqlDataSortDraft{Column: "label", Direction: "升序"})
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, doc)
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
	doc.page = nil
	doc.err = strings.Repeat("query failed ", 40)
	tt.Frame()
	for _, name := range []string{"重试读取数据", "应用筛选", "下一页数据"} {
		r, ok := tt.Find(name)
		if !ok || r.X+r.W > 384 || r.Y+r.H > 410 {
			t.Fatalf("failure state hid %s: %+v", name, r)
		}
	}
}

func TestDataRowHeaderKeepsFieldMenusAndCellCopyMapped(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, doc)
	}, 540, 320)
	if tt.HasText("#") {
		t.Fatal("row numbers should be optional")
	}
	cell, ok := tt.Find("safe-value")
	if !ok {
		t.Fatal("data cell is missing")
	}
	tt.ClickAt(mysqlDataRowHeaderColumn(doc).Width/2, cell.Y+cell.H/2)
	if !doc.grid.Cells.All || doc.selectedRow != 0 {
		t.Fatal("narrow row selector did not choose the whole row")
	}
	if err := tt.Click("safe-value"); err != nil {
		t.Fatal(err)
	}
	if err := clickDataToolbar(tt, "复制所选数据预览"); err != nil {
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

func mysqlInspectorSampleTab() *Document {
	literalNull := "NULL"
	length := int64(80)
	precision, scale := int64(10), int64(2)
	charset, collation := "utf8mb4", "utf8mb4_0900_ai_ci"
	return &Document{
		database: "test_schema", table: "entries", selectedRow: -1,
		page: &mysqlsvc.MySQLDataPage{
			Columns:     []string{"id", "label", "price"},
			ColumnKinds: []string{"bigint", "varchar", "decimal"},
			ColumnMetadata: []mysqlsvc.MySQLStructureColumn{
				{Name: "id", DataType: "bigint", Type: "bigint unsigned", Key: "PRI", Extra: "auto_increment"},
				{Name: "label", DataType: "varchar", Type: "varchar(80)", Nullable: true,
					Default: &literalNull, CharacterMaxLength: &length, CharacterSet: &charset,
					Collation: &collation, Comment: "reviewed"},
				{Name: "price", DataType: "decimal", Type: "decimal(10,2)", NumericPrecision: &precision,
					NumericScale: &scale, Nullable: true},
			},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "sample"}, {Text: "12.50"}}},
		},
	}
}
