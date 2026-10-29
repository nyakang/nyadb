package connectioncatalog

import (
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestResetClearsDraftAndPreservesChooserHistory(t *testing.T) {
	s := New()
	s.chooseCatalogEntry(databaseCatalog[0], nil)
	s.RecordRecent("MySQL")
	s.gridMode = false
	s.treeOpen["华为"] = true
	s.search = "Aurora"
	setCatalogFilter(s.engineFilters, "MySQL", true)
	setCatalogFilter(s.vendorFilters, "AWS", true)
	icons := s.bundledIcons
	s.Reset()
	if s.Selected() != (Choice{}) || s.search != "" || len(s.engineFilters) != 0 || len(s.vendorFilters) != 0 {
		t.Fatal("reset must clear the chooser draft")
	}
	if s.gridMode || !s.treeOpen["华为"] || !slices.Equal(s.recent, []string{"MySQL"}) || s.bundledIcons["MySQL"] != icons["MySQL"] {
		t.Fatal("reset must retain presentation, expansion, recents and decoded icons")
	}
	setCatalogFilter(s.engineFilters, "SQLite", true)
	other := New()
	if len(other.engineFilters) != 0 || len(other.recent) != 0 || !other.gridMode || other.treeOpen["华为"] {
		t.Fatal("chooser instances must not share interaction state")
	}
}

func TestRecentConnectionsDeduplicateAndKeepFiveNewest(t *testing.T) {
	s := New()
	s.chooseCatalogEntry(databaseCatalog[0], nil)
	if len(s.recent) != 0 {
		t.Fatal("selection alone must not record actual use")
	}
	for _, name := range []string{"MySQL", "PostgreSQL", "SQLite", "Oracle", "Redis", "MongoDB", "SQLite"} {
		s.RecordRecent(name)
	}
	want := []string{"SQLite", "MongoDB", "Redis", "Oracle", "PostgreSQL"}
	if !slices.Equal(s.recent, want) {
		t.Fatalf("recent order: %v, want %v", s.recent, want)
	}
	items := s.catalogRecents()
	if len(items) != len(want) {
		t.Fatalf("recent catalog items: %v", items)
	}
	for i, item := range items {
		if item.name != want[i] {
			t.Errorf("recent item %d: %q, want %q", i, item.name, want[i])
		}
	}
	s.search = "sqlite"
	if items := s.catalogRecents(); len(items) != 1 || items[0].name != "SQLite" {
		t.Fatalf("recents must respect the current search: %v", items)
	}
}

func TestConnectionCatalogFiltering(t *testing.T) {
	s := New()
	if n := len(s.catalogItems("全部")); n != 11 {
		t.Fatalf("catalog should offer 11 local types, got %d", n)
	}
	s.search = "sql"
	if items := s.catalogItems("全部"); len(items) != 4 {
		t.Fatalf("search should include MySQL/PostgreSQL/SQL Server/SQLite, got %+v", items)
	}
	s.search = ""
	setCatalogFilter(s.engineFilters, "PostgreSQL", true)
	if items := s.catalogItems("全部"); len(items) != 1 || items[0].kind != connections.PostgreSQL {
		t.Fatalf("engine filter: %+v", items)
	}
	setCatalogFilter(s.vendorFilters, "阿里云", true)
	if items := s.catalogItems("云数据库"); len(items) < 2 || items[0].engine != "PostgreSQL" {
		t.Fatalf("vendor and engine intersection: %+v", items)
	}
	s.clearCatalogFilters()
	if len(s.catalogItems("云数据库")) < 5 {
		t.Fatal("reset did not clear filters")
	}
}

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

func TestExpandedCatalogProviderFilters(t *testing.T) {
	s := New()
	s.search = "Aurora"
	got := s.catalogItems("云数据库")
	if len(got) != 2 {
		t.Fatalf("Aurora search: %+v", got)
	}
	s.clearCatalogFilters()
	setCatalogFilter(s.vendorFilters, "华为", true)
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
}

func TestFilterIconsForEnginesAndProviders(t *testing.T) {
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
}

func TestProviderSidebarMatchesTreeAndEmbeddedBadges(t *testing.T) {
	if len(catalogVendors) != len(catalogTreeVendorOrder) {
		t.Fatalf("sidebar vendor count %d differs from tree %d", len(catalogVendors), len(catalogTreeVendorOrder))
	}
	s := New()
	for index, vendor := range catalogTreeVendorOrder {
		if catalogVendors[index] != vendor {
			t.Fatalf("sidebar order mismatch at %d: %q vs %q", index, catalogVendors[index], vendor)
		}
		if s.providerIcons[vendor] == nil {
			t.Errorf("missing embedded badge for %q", vendor)
		}
	}
	for vendor, display := range map[string]string{
		"亚信星辰":    "平凯星辰",
		"MongoDB": "MongoDB Atlas",
		"Redis":   "Redis Enterprise Cloud",
	} {
		if catalogTreeVendorLabels[vendor] != display {
			t.Errorf("wrong vendor display for %s: %s", vendor, catalogTreeVendorLabels[vendor])
		}
	}
}

func TestDamengVendorFilterIsNotEmpty(t *testing.T) {
	s := New()
	setCatalogFilter(s.vendorFilters, "达梦", true)
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].name != "达梦" {
		t.Fatalf("Dameng vendor checkbox should not hide all choices: %+v", items)
	}
	groups := s.catalogTreeGroups()
	if len(groups) != 1 || groups[0].vendor != "达梦" || len(groups[0].items) != 1 {
		t.Fatalf("Dameng tree group absent with active filter: %+v", groups)
	}
}
