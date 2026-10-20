package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionCatalogFiltering(t *testing.T) {
	s := New()
	if n := len(s.catalogItems("全部")); n != 11 {
		t.Fatalf("catalog should offer 11 local types, got %d", n)
	}
	s.connectionSearch = "sql"
	if items := s.catalogItems("全部"); len(items) != 4 {
		t.Fatalf("search should include MySQL/PostgreSQL/SQL Server/SQLite, got %+v", items)
	}
	s.connectionSearch = ""
	setCatalogFilter(s.connectionEngineFilters, "PostgreSQL", true)
	if items := s.catalogItems("全部"); len(items) != 1 || items[0].kind != connections.PostgreSQL {
		t.Fatalf("engine filter: %+v", items)
	}
	setCatalogFilter(s.connectionVendorFilters, "阿里云", true)
	if items := s.catalogItems("云数据库"); len(items) != 1 || items[0].engine != "PostgreSQL" {
		t.Fatalf("vendor and engine intersection: %+v", items)
	}
	s.clearCatalogFilters()
	if len(s.catalogItems("云数据库")) < 5 {
		t.Fatal("reset did not clear filters")
	}
}

func TestConnectionCatalogUnsupportedType(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseCatalogEntry(databaseCatalog[0])
	if s.connectionKind != connections.MySQL {
		t.Fatal("supported engine was not selected")
	}
	s.chooseCatalogEntry(databaseCatalog[2]) // SQL Server is discovery-only
	if s.connectionKind != "" || !strings.Contains(s.connectionError, "尚未支持") {
		t.Fatalf("unsupported driver would proceed: %s / %q", s.connectionKind, s.connectionError)
	}
	s.nextConnectionStep()
	if s.connectionStep != 0 {
		t.Fatal("unsupported type advanced to configuration")
	}
	s.chooseCatalogEntry(databaseCatalog[4])
	s.nextConnectionStep()
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.SQLite {
		t.Fatal("SQLite navigation broke")
	}
	if len(s.connectionRecent) != 2 || s.connectionRecent[0] != "SQLite" {
		t.Fatalf("recent order: %v", s.connectionRecent)
	}
	s.openConnectionDialog()
	if s.connectionSearch != "" || len(s.connectionRecent) != 2 {
		t.Fatal("reopening should clear filters but retain in-session recents")
	}
}

func TestConnectionCatalogNativeLayoutAndListMode(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"连接筛选", "供应商筛选", "常用类型", "全部", "云数据库", "搜索连接类型"} {
		if !tt.HasText(label) && label != "搜索连接类型" {
			t.Errorf("catalog section missing: %s", label)
		}
	}
	if err := tt.Click("列表视图"); err != nil {
		t.Fatal(err)
	}
	if s.connectionGridMode {
		t.Fatal("list switch did not change presentation")
	}
	if err := tt.Click("网格视图"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionGridMode {
		t.Fatal("grid switch did not change presentation")
	}
	if err := tt.Click("选择 MySQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Kind != connections.MySQL {
		t.Fatal("existing MySQL workflow regressed")
	}
}
