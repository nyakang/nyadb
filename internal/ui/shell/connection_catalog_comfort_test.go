package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestConnectionChooserListRowComfort(t *testing.T) {
	// Maintain a generous hit target for each row. The original ~19px tree
	// was accurate structurally but too dense for the NyaDB native chooser.
	if catalogTreeRowHeight < 24 || catalogTreeRowHeight > 30 {
		t.Fatalf("tree rows should have comfortable spacing, got %v", catalogTreeRowHeight)
	}
}

func TestConnectionChooserFiltersWithoutPermanentResetButton(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("重置") {
		t.Fatal("the chooser must not show a permanent Reset button")
	}
	if err := tt.Click("筛选 MySQL"); err != nil {
		t.Fatalf("enable filter: %v", err)
	}
	if !s.connectionEngineFilters["MySQL"] {
		t.Fatal("checkbox must enable the filter")
	}
	if err := tt.Click("筛选 MySQL"); err != nil {
		t.Fatalf("disable filter: %v", err)
	}
	if s.connectionEngineFilters["MySQL"] {
		t.Fatal("checkbox must directly clear its filter, no Reset button needed")
	}
}
