package shell

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLNativeDataFilterSortCopyAndWidths(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var mu sync.Mutex
	var seen []connections.MySQLDataOptions
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, db, table string, o connections.MySQLDataOptions) (*connections.MySQLDataPage, error) {
		mu.Lock()
		seen = append(seen, o)
		mu.Unlock()
		return &connections.MySQLDataPage{
			Database: db, Table: table, Offset: o.Offset, Limit: o.Limit,
			Columns:     []string{"id", "note"},
			ColumnKinds: []string{"bigint", "varchar"}, StableOrder: true, HasMore: true,
			Rows: [][]connections.MySQLDataCell{{{Text: "3"}, {Text: "hello"}}},
		}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	current := s.currentTab()
	current.dataFilterColumnDraft = "note"
	current.dataFilterValueDraft = "hello"
	current.dataFilterModeDraft = "包含"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	mu.Lock()
	filtered := seen[len(seen)-1]
	mu.Unlock()
	if filtered.FilterColumn != "note" || filtered.FilterMode != "contains" ||
		filtered.FilterValue != "hello" || filtered.Offset != 0 {
		t.Fatalf("filter not sent to service: %+v", filtered)
	}
	// The native header sorts by its column ID; check that a new server query
	// is issued rather than locally reordering one page.
	current.dataSort = ui.SortOrder{Column: "note", Descending: true}
	tt.Frame()
	waitMySQLData(t, s, tt)
	mu.Lock()
	sorted := seen[len(seen)-1]
	mu.Unlock()
	if sorted.SortColumn != "note" || !sorted.SortDescending || sorted.FilterMode != "contains" {
		t.Fatalf("sort not forwarded with active filter: %+v", sorted)
	}
	current.dataSelectedRow = 0
	current.dataState.Cells = ui.ColumnRange{Cursor: "note", Anchor: "note"}
	tt.Frame()
	if err := tt.Click("复制所选数据预览"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "hello" {
		t.Fatalf("clipboard: %q", tt.Clipboard())
	}
	if err := tt.Click("查看字段信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("数据字段信息面板") {
		t.Fatalf("inspector missing: %v", tt.Texts())
	}
	current.dataState.Columns.Widths = map[string]float32{"note": 280}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if current.dataState.Columns.Widths["note"] != 280 {
		t.Fatal("column width lost on paging")
	}
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	mu.Lock()
	cleared := seen[len(seen)-1]
	mu.Unlock()
	if cleared.FilterMode != "" || cleared.Offset != 0 {
		t.Fatalf("filter was not cleared: %+v", cleared)
	}
}
