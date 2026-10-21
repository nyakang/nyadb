package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestExpandedCatalogMirrorsObservedGroups(t *testing.T) {
	if len(databaseCatalog) < 60 {
		t.Fatalf("expected expanded connection browser with at least 60 choices, got %d", len(databaseCatalog))
	}
	if len(catalogVendors) < 12 || len(catalogEngines) != 9 {
		t.Fatalf("missing engine/vendor filters: %d/%d", len(catalogEngines), len(catalogVendors))
	}
	seen := map[string]bool{}
	for _, item := range databaseCatalog {
		if seen[item.name] {
			t.Errorf("duplicate catalog entry: %s", item.name)
		}
		seen[item.name] = true
		if item.section == "云数据库" && item.kind != "" {
			t.Errorf("unsupported cloud type must not advertise a working driver: %s", item.name)
		}
	}
	for _, name := range []string{
		"Amazon Aurora for MySQL", "华为云 GaussDB(DWS)", "阿里云 PolarDB MySQL 版",
		"MongoDB Atlas", "Microsoft Garnet", "Redis Enterprise Cloud",
		"Google Cloud SQL for PostgreSQL", "OceanBase 企业版（Oracle 模式）",
	} {
		if !seen[name] {
			t.Errorf("missing screenshot catalog choice: %s", name)
		}
	}
}

func TestExpandedCatalogProviderFiltersAndBlockedNext(t *testing.T) {
	s := New()
	s.connectionSearch = "Aurora"
	got := s.catalogItems("云数据库")
	if len(got) != 2 {
		t.Fatalf("Aurora search: %+v", got)
	}
	s.clearCatalogFilters()
	setCatalogFilter(s.connectionVendorFilters, "华为", true)
	got = s.catalogItems("云数据库")
	if len(got) < 5 {
		t.Fatalf("Huawei cloud group incomplete: %d", len(got))
	}
	for _, item := range got {
		if item.vendor != "华为" {
			t.Fatal("provider filter leaked an item")
		}
	}
	s.clearCatalogFilters()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	// Programmatic selection of a non-functional type is intentionally
	// rejected by the same state model used by the clickable native tile.
	for _, item := range databaseCatalog {
		if item.name == "MongoDB Atlas" {
			s.chooseCatalogEntry(item)
			break
		}
	}
	s.nextConnectionStep()
	if s.connectionStep != 0 || !strings.Contains(s.connectionError, "尚未支持") &&
		!strings.Contains(s.connectionError, "请选择") {
		t.Fatalf("unsupported catalog type advanced to profile editor: step=%d err=%q", s.connectionStep, s.connectionError)
	}
}

func TestFilterIconsAndIconOnlyViewControls(t *testing.T) {
	for _, name := range []string{"MySQL", "PostgreSQL", "SQLite", "Oracle"} {
		if _, ok := catalogFilterIcon(name, false); !ok {
			t.Errorf("missing small icon: %s", name)
		}
	}
	for _, name := range []string{"阿里云", "华为", "腾讯云", "AWS"} {
		if _, ok := catalogFilterIcon(name, true); !ok {
			t.Errorf("missing provider icon: %s", name)
		}
	}
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("列表视图"); err != nil {
		t.Fatal(err)
	}
	if s.connectionGridMode {
		t.Fatal("list control did not change view")
	}
	if err := tt.Click("网格视图"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionGridMode {
		t.Fatal("grid control did not change view")
	}
}
