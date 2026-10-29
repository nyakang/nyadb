package connectioncatalog

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNavicatObservedTreeGroupHierarchy(t *testing.T) {
	s := New()
	if got := s.catalogTreeDirect(); len(got) != 9 {
		t.Fatalf("Navicat list mode has nine direct database rows; got %d", len(got))
	}
	groups := s.catalogTreeGroups()
	if len(groups) < 12 || groups[0].vendor != "阿里云" || groups[1].vendor != "华为" {
		t.Fatalf("vendor order mismatch: %+v", groups)
	}
	for _, group := range groups {
		if len(group.items) == 0 {
			t.Errorf("empty vendor node %q", group.label)
		}
		for _, entry := range group.items {
			if entry.vendor != group.vendor && !(group.vendor == "达梦" && entry.engine == "达梦" && entry.section == "全部") {
				t.Errorf("%q appeared under %q", entry.name, group.label)
			}
		}
	}
}

func TestNavicatObservedTreeExpansionAndSelection(t *testing.T) {
	s := New()
	tt := ui.NewTester(catalogTestView(s), 860, 566)
	if err := tt.Click("列表视图"); err != nil {
		t.Fatal(err)
	}
	if s.treeOpen["华为"] {
		t.Fatal("vendors should start collapsed")
	}
	if err := tt.Click("展开 华为"); err != nil {
		t.Fatal(err)
	}
	if !s.treeOpen["华为"] || s.treeOpen["阿里云"] {
		t.Fatal("expand toggled wrong vendor")
	}
	if err := tt.Click("选择 华为云 GaussDB 主备版"); err != nil {
		t.Fatal(err)
	}
	if s.selected.Name != "华为云 GaussDB 主备版" {
		t.Fatalf("tree leaf selection failed: selected %q", s.selected.Name)
	}
	if err := tt.Click("展开 华为"); err != nil {
		t.Fatal(err)
	}
	if s.treeOpen["华为"] {
		t.Fatal("second click did not collapse the vendor")
	}
}

func TestNavicatObservedTreeSearchExpandsWithoutOverwritingState(t *testing.T) {
	s := New()
	s.search = "Gauss"
	direct := s.catalogTreeDirect()
	if !s.catalogSidebarFilterVisible("华为", true) {
		t.Fatal("Gauss search should retain Huawei in sidebar")
	}
	if s.catalogSidebarFilterVisible("MySQL", false) ||
		s.catalogSidebarFilterVisible("阿里云", true) {
		t.Fatal("sidebar must filter unrelated engines and vendors like original")
	}
	if len(direct) != 0 {
		t.Fatalf("unexpected direct matches: %+v", direct)
	}
	groups := s.catalogTreeGroups()
	if len(groups) != 1 || groups[0].vendor != "华为" || len(groups[0].items) < 3 {
		t.Fatalf("expected filtered Huawei GaussDB leaves: %+v", groups)
	}
	if s.treeOpen["华为"] {
		t.Fatal("search should not persist the auto-expanded state")
	}
	s.search = ""
	if len(s.catalogTreeGroups()) < 12 || s.treeOpen["华为"] {
		t.Fatal("clearing search must restore collapsed state and full catalog")
	}
}
