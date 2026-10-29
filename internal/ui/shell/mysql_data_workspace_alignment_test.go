package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLDataFooterNeverInventsQueryResults(t *testing.T) {
	doc := &tab{kind: tabMySQLData, dataTable: "orders", dataLimit: 2}
	if got := mysqlDataPageStatus(doc); got != "尚未读取数据" {
		t.Fatalf("unloaded data displayed success: %q", got)
	}
	doc.dataLoading = true
	if got := mysqlDataPageStatus(doc); got != "正在读取第 1 页…" {
		t.Fatalf("loading displayed success: %q", got)
	}
	doc.dataLoading = false
	doc.dataErr = "permission denied"
	if got := mysqlDataPageStatus(doc); got != "读取失败 · 第 1 页" {
		t.Fatalf("failure displayed success: %q", got)
	}
	doc.dataErr = ""
	doc.dataPage = &mysqlsvc.MySQLDataPage{}
	if got := mysqlDataPageStatus(doc); got != "已读取 0 行 · 第 1 页" {
		t.Fatalf("confirmed empty result wrong: %q", got)
	}
	doc.dataOffset = 2
	doc.dataPage.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "3"}}}
	if got := mysqlDataPageStatus(doc); got != "已读取 1 行 · 3–3 · 第 2 页" {
		t.Fatalf("real page range wrong: %q", got)
	}
}

func TestMySQLNativeDataFooterAndGridRemainVisible(t *testing.T) {
	s := New()
	doc := alignedDataTestTab()
	doc.dataLimit = 1
	doc.dataOffset = 3
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataWorkspace(c, doc)
	}, 840, 560)
	if !tt.HasText("已读取 1 行 · 4–4 · 第 4 页") ||
		!tt.HasText("safe-value") || !tt.HasText("下一页数据") {
		t.Fatalf("missing confirmed row range or grid: %v", tt.Texts())
	}
	doc.dataLoading = true
	tt.Frame()
	if !tt.HasText("正在读取第 4 页…") ||
		tt.HasText("已读取 1 行 · 4–4 · 第 4 页") {
		t.Fatalf("loading displayed stale success: %v", tt.Texts())
	}
}

func TestMySQLWorkspaceCaptionsKeepQualifiedTabIdentity(t *testing.T) {
	s := New()
	data := &tab{kind: tabMySQLData, title: "first.orders · 数据",
		dataDatabase: "first", dataTable: "orders"}
	design := &tab{kind: tabStructure, title: "first.orders",
		structureDatabase: "first", structureTable: "orders"}
	s.tabs = []*tab{data, design}
	if s.workspaceTabCaption(data) != "orders" || s.workspaceTabCaption(design) != "orders · 设计" {
		t.Fatalf("compact tab captions lost data/design distinction")
	}
	other := &tab{kind: tabMySQLData, title: "second.orders · 数据",
		dataDatabase: "second", dataTable: "orders"}
	s.tabs = append(s.tabs, other)
	if s.workspaceTabCaption(data) != "first.orders" ||
		s.workspaceTabCaption(design) != "first.orders · 设计" ||
		s.workspaceTabCaption(other) != "second.orders" {
		t.Fatalf("cross-database same-name tabs were ambiguous")
	}
	if data.title != "first.orders · 数据" || other.title != "second.orders · 数据" {
		t.Fatal("caption change modified qualified tab identity or close label")
	}
}
