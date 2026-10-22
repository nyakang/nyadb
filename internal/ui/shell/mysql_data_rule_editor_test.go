package shell

import (
	"context"
	"sync"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLRuleEditorMultipleConditionsPrioritySortAndMenu(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var mu sync.Mutex
	var observed []connections.MySQLDataOptions
	s.mysqlDataAdvancedLoader = func(ctx context.Context, id int64, db, table string, o connections.MySQLDataOptions) (*connections.MySQLDataPage, error) {
		mu.Lock()
		observed = append(observed, o)
		mu.Unlock()
		return &connections.MySQLDataPage{Database: db, Table: table, Offset: o.Offset, Limit: o.Limit,
			Columns: []string{"id", "note", "score"}, ColumnKinds: []string{"bigint", "varchar", "int"},
			Rows:        [][]connections.MySQLDataCell{{{Text: "1"}, {Text: "hello"}, {Text: "2"}}},
			StableOrder: true}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	tab := s.currentTab()
	if !tt.HasText("筛选 & 排序") {
		t.Fatalf("rule editor missing: %v", tt.Texts())
	}
	tab.dataFilterColumnDraft = "note"
	tab.dataFilterModeDraft = "包含"
	tab.dataFilterValueDraft = "he"
	if err := tt.Click("添加筛选规则"); err != nil {
		t.Fatal(err)
	}
	tab.dataFilterExtraDraft[0] = mysqlDataRuleDraft{Column: "score", Mode: "精确匹配", Value: "2"}
	if err := tt.Click("添加排序"); err != nil {
		t.Fatal(err)
	}
	tab.dataSortDraft[0] = mysqlDataSortDraft{Column: "score", Direction: "降序"}
	if err := tt.Click("添加排序"); err != nil {
		t.Fatal(err)
	}
	tab.dataSortDraft[1] = mysqlDataSortDraft{Column: "id", Direction: "升序"}
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	mu.Lock()
	last := observed[len(observed)-1]
	mu.Unlock()
	if len(last.Filters) != 2 || last.Filters[0].Column != "note" ||
		last.Filters[1].Value != "2" || len(last.Sorts) != 2 ||
		last.Sorts[0].Column != "score" || !last.Sorts[0].Descending ||
		last.Sorts[1].Column != "id" || last.Offset != 0 {
		t.Fatalf("rules not sent to real query loader: %+v", last)
	}
	if err := tt.RightClick("id · bigint"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("隐藏此列"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	indices, cols := mysqlVisibleDataColumns(tab, tab.dataPage)
	if len(indices) != 2 || indices[0] != 1 || cols[0].ID != "note" || !tab.dataHiddenColumns["id"] {
		t.Fatalf("column index mapping broke: %+v %+v", indices, cols)
	}
	if err := tt.RightClick("note · varchar"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("显示/隐藏列", "id"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	indices, _ = mysqlVisibleDataColumns(tab, tab.dataPage)
	if len(indices) != 3 || tab.dataHiddenColumns["id"] {
		t.Fatal("restore hidden column failed")
	}
	if err := tt.RightClick("id · bigint"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制字段名称"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "id" {
		t.Fatalf("copy header: %q", tt.Clipboard())
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if tab.dataRulesExpanded {
		t.Fatal("rules panel did not collapse")
	}
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	if !tab.dataRulesExpanded {
		t.Fatal("rules panel did not expand")
	}
}
