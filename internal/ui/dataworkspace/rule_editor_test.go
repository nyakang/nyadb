package dataworkspace

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLRuleEditorMultipleConditionsPrioritySortAndMenu(t *testing.T) {
	s, doc, tt := dataTestWorkspace(t)
	var mu sync.Mutex
	var observed []mysqlsvc.MySQLDataOptions
	s.AdvancedLoader = func(ctx context.Context, id int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		mu.Lock()
		observed = append(observed, o)
		mu.Unlock()
		return &mysqlsvc.MySQLDataPage{Database: db, Table: table, Offset: o.Offset, Limit: o.Limit,
			Columns: []string{"id", "note", "score"}, ColumnKinds: []string{"bigint", "varchar", "int"},
			Rows:        [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "hello"}, {Text: "2"}}},
			StableOrder: true}, nil
	}
	s.Load(ui.Services{}, doc, false)
	tt.Frame()
	waitMySQLData(t, s, doc, tt)
	Document := doc
	if Document.rulesExpanded || len(Document.filterRules) != 0 || len(Document.sortRules) != 0 ||
		tt.HasText("筛选  ·  所有已启用条件均满足 (AND)") {
		t.Fatalf("new Document should start compact: %v", tt.Texts())
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if !Document.rulesExpanded {
		t.Fatal("failed to expand the complete rule editor")
	}
	Document.filterColumnDraft = "note"
	Document.filterModeDraft = "包含"
	Document.filterValueDraft = "he"
	if err := tt.Click("添加筛选规则"); err != nil {
		t.Fatal(err)
	}
	Document.filterExtraDraft[0] = mysqlDataRuleDraft{Column: "score", Mode: "精确匹配", Value: "2"}
	if err := tt.Click("添加排序"); err != nil {
		t.Fatal(err)
	}
	Document.sortDraft[0] = mysqlDataSortDraft{Column: "score", Direction: "降序"}
	if err := tt.Click("添加排序"); err != nil {
		t.Fatal(err)
	}
	Document.sortDraft[1] = mysqlDataSortDraft{Column: "id", Direction: "升序"}
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	mu.Lock()
	last := observed[len(observed)-1]
	mu.Unlock()
	if len(last.Filters) != 2 || last.Filters[0].Column != "note" ||
		last.Filters[1].Value != "2" || len(last.Sorts) != 2 ||
		last.Sorts[0].Column != "score" || !last.Sorts[0].Descending ||
		last.Sorts[1].Column != "id" || last.Offset != 0 {
		t.Fatalf("rules not sent to real query loader: %+v", last)
	}
	if err := tt.RightClick("bigint"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("隐藏此列"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	indices, cols := mysqlVisibleDataColumns(Document, Document.page)
	if len(indices) != 2 || indices[0] != 1 || cols[0].ID != "note" || !Document.hiddenColumns["id"] {
		t.Fatalf("column index mapping broke: %+v %+v", indices, cols)
	}
	if err := tt.RightClick("varchar"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("显示/隐藏列", "id"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	indices, _ = mysqlVisibleDataColumns(Document, Document.page)
	if len(indices) != 3 || Document.hiddenColumns["id"] {
		t.Fatal("restore hidden column failed")
	}
	if err := tt.RightClick("bigint"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制字段名称"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "id" {
		t.Fatalf("copy header: %q", tt.Clipboard())
	}
	if err := tt.RightClick("bigint"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("显示字段类型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if Document.showFieldTypes || tt.HasText("bigint") {
		t.Fatal("subtitle visibility toggle did not affect native Table header")
	}
	// Rebuild the subtitle through the same native Table state,
	// without resetting layout widths or sort state.
	Document.showFieldTypes = true
	tt.Frame()
	if !Document.showFieldTypes || !tt.HasText("bigint") {
		t.Fatal("subtitle was not restored")
	}
	before := Document.filterExtraDraft[0]
	width := Document.grid.Columns.Widths["note"]
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if Document.rulesExpanded || tt.HasText("筛选  ·  所有已启用条件均满足 (AND)") ||
		len(Document.filterRules) != 2 || len(Document.sortRules) != 2 {
		t.Fatalf("collapsed applied-rule summary incorrect: %v", tt.Texts())
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if !Document.rulesExpanded || Document.filterExtraDraft[0] != before ||
		Document.grid.Columns.Widths["note"] != width || !tt.HasText("筛选  ·  所有已启用条件均满足 (AND)") {
		t.Fatal("reopen discarded drafts or layout state")
	}
}
