package dataworkspace

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLNativeDataFilterSortCopyAndWidths(t *testing.T) {
	s, doc, tt := dataTestWorkspace(t)
	var mu sync.Mutex
	var seen []mysqlsvc.MySQLDataOptions
	s.AdvancedLoader = func(_ context.Context, _ int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		mu.Lock()
		seen = append(seen, o)
		mu.Unlock()
		return &mysqlsvc.MySQLDataPage{
			Database: db, Table: table, Offset: o.Offset, Limit: o.Limit,
			Columns:     []string{"id", "note"},
			ColumnKinds: []string{"bigint", "varchar"}, StableOrder: true, HasMore: true,
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "3"}, {Text: "hello"}}},
		}, nil
	}
	s.Load(ui.Services{}, doc, false)
	tt.Frame()
	waitMySQLData(t, s, doc, tt)
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	current := doc
	current.filterColumnDraft = "note"
	current.filterValueDraft = "hello"
	current.filterModeDraft = "包含"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	mu.Lock()
	filtered := seen[len(seen)-1]
	mu.Unlock()
	if filtered.FilterColumn != "note" || filtered.FilterMode != "contains" ||
		filtered.FilterValue != "hello" || filtered.Offset != 0 {
		t.Fatalf("filter not sent to service: %+v", filtered)
	}
	// The native header sorts by its column ID; check that a new server query
	// is issued rather than locally reordering one page.
	current.sort = ui.SortOrder{Column: "note", Descending: true}
	tt.Frame()
	waitMySQLData(t, s, doc, tt)
	mu.Lock()
	sorted := seen[len(seen)-1]
	mu.Unlock()
	if sorted.SortColumn != "note" || !sorted.SortDescending || sorted.FilterMode != "contains" {
		t.Fatalf("sort not forwarded with active filter: %+v", sorted)
	}
	current.selectedRow = 0
	current.grid.Cells = ui.ColumnRange{Cursor: "note", Anchor: "note"}
	tt.Frame()
	if err := clickDataToolbar(tt, "复制所选数据预览"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "hello" {
		t.Fatalf("clipboard: %q", tt.Clipboard())
	}
	if err := clickDataToolbar(tt, "查看字段信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("数据字段信息面板") {
		t.Fatalf("inspector missing: %v", tt.Texts())
	}
	current.grid.Columns.Widths = map[string]float32{"note": 280}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if current.grid.Columns.Widths["note"] != 280 {
		t.Fatal("column width lost on paging")
	}
	if err := clickDataToolbar(tt, "清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	mu.Lock()
	cleared := seen[len(seen)-1]
	mu.Unlock()
	if cleared.FilterMode != "" || cleared.Offset != 0 {
		t.Fatalf("filter was not cleared: %+v", cleared)
	}
}
