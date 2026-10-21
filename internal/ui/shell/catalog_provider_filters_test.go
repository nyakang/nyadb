package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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
	setCatalogFilter(s.connectionVendorFilters, "达梦", true)
	items := s.catalogItems("全部")
	if len(items) != 1 || items[0].name != "达梦" {
		t.Fatalf("Dameng vendor checkbox should not hide all choices: %+v", items)
	}
	groups := s.catalogTreeGroups()
	if len(groups) != 1 || groups[0].vendor != "达梦" || len(groups[0].items) != 1 {
		t.Fatalf("Dameng tree group absent with active filter: %+v", groups)
	}
	if err := ui.NewTester(s.View, 1280, 800).Click("连接"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderSidebarShowsDescriptiveCategoryLabels(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("连接筛选") || !tt.HasText("供应商筛选") {
		t.Fatal("sidebar category headings are not present")
	}
	if err := tt.Click("筛选供应商 华为"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionVendorFilters["华为"] {
		t.Fatal("vendor checkbox not wired")
	}
	if err := tt.Click("筛选供应商 华为"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionVendorFilters) != 0 {
		t.Fatal("vendor filter did not clear")
	}
}
