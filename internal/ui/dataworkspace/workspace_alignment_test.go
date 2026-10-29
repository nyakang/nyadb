package dataworkspace

import (
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataFooterNeverInventsQueryResults(t *testing.T) {
	doc := &Document{table: "orders", limit: 2}
	if got := mysqlDataPageStatus(doc); got != "尚未读取数据" {
		t.Fatalf("unloaded data displayed success: %q", got)
	}
	doc.loading = true
	if got := mysqlDataPageStatus(doc); got != "正在读取第 1 页…" {
		t.Fatalf("loading displayed success: %q", got)
	}
	doc.loading = false
	doc.err = "permission denied"
	if got := mysqlDataPageStatus(doc); got != "读取失败 · 第 1 页" {
		t.Fatalf("failure displayed success: %q", got)
	}
	doc.err = ""
	doc.page = &mysqlsvc.MySQLDataPage{}
	if got := mysqlDataPageStatus(doc); got != "已读取 0 行 · 第 1 页" {
		t.Fatalf("confirmed empty result wrong: %q", got)
	}
	doc.offset = 2
	doc.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "3"}}}
	if got := mysqlDataPageStatus(doc); got != "已读取 1 行 · 3–3 · 第 2 页" {
		t.Fatalf("real page range wrong: %q", got)
	}
}

func TestMySQLNativeDataFooterAndGridRemainVisible(t *testing.T) {
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	doc.limit = 1
	doc.offset = 3
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, doc)
	}, 840, 560)
	if !tt.HasText("已读取 1 行 · 4–4 · 第 4 页") ||
		!tt.HasText("safe-value") || !tt.HasText("下一页数据") {
		t.Fatalf("missing confirmed row range or grid: %v", tt.Texts())
	}
	doc.loading = true
	tt.Frame()
	if !tt.HasText("正在读取第 4 页…") ||
		tt.HasText("已读取 1 行 · 4–4 · 第 4 页") {
		t.Fatalf("loading displayed stale success: %v", tt.Texts())
	}
}
