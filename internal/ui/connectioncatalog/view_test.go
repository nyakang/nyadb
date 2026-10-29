package connectioncatalog

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSelectionCallbackUsesTheCurrentChoice(t *testing.T) {
	for _, grid := range []bool{true, false} {
		name := "grid"
		if !grid {
			name = "tree"
		}
		t.Run(name, func(t *testing.T) {
			s := New()
			s.gridMode = grid
			var choices []Choice
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c)
				width, height := c.Size()
				s.View(c, width, height, func(choice Choice) {
					if s.Selected() != choice {
						t.Error("host callback saw stale selection")
					}
					choices = append(choices, choice)
				})
			}, 900, 657)
			if len(choices) != 0 {
				t.Fatal("rendering must not trigger selection")
			}
			for _, label := range []string{"选择 MySQL", "选择 SQL Server"} {
				if grid && label == "选择 SQL Server" {
					label += "（尚未支持）"
				}
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			if len(choices) != 2 || choices[0] != (Choice{Name: "MySQL", Kind: connections.MySQL}) || choices[1] != (Choice{Name: "SQL Server"}) {
				t.Fatalf("selection callbacks: %v", choices)
			}
			if len(s.recent) != 0 {
				t.Fatal("selection must not move cards by adding recents")
			}
		})
	}
}

func catalogTestView(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c)
		width, height := c.Size()
		s.View(c, width, height, nil)
	}
}

func TestConnectionChooserCompactRowsRemainIndividuallySelectable(t *testing.T) {
	s := New()
	s.gridMode = false
	tt := ui.NewTester(catalogTestView(s), 900, 657)
	previousY := float32(-1)
	for _, name := range []string{"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite"} {
		r, ok := tt.Find("选择 " + name)
		if !ok || r.H != 19 || (previousY >= 0 && r.Y-previousY != 19) {
			t.Fatalf("%s does not occupy a compact non-overlapping row: %+v", name, r)
		}
		previousY = r.Y
	}
	for _, name := range []string{"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite"} {
		r, ok := tt.Find("选择 " + name)
		if !ok {
			t.Fatalf("missing %s after selecting another type", name)
		}
		// Hit the lower edge as well as the row center: neighboring rows
		// must not overlap after reducing the icon and text line heights.
		tt.ClickAt(r.X+r.W/2, r.Y+r.H-1)
		if s.selected.Name != name {
			t.Fatalf("clicking %s selected %q", name, s.selected.Name)
		}
	}
}

func TestConnectionChooserFiltersWithoutPermanentResetButton(t *testing.T) {
	s := New()
	tt := ui.NewTester(catalogTestView(s), 860, 566)
	if tt.HasText("重置") {
		t.Fatal("the chooser must not show a permanent Reset button")
	}
	if err := tt.Click("筛选 MySQL"); err != nil {
		t.Fatalf("enable filter: %v", err)
	}
	if !s.engineFilters["MySQL"] {
		t.Fatal("checkbox must enable the filter")
	}
	if err := tt.Click("筛选 MySQL"); err != nil {
		t.Fatalf("disable filter: %v", err)
	}
	if s.engineFilters["MySQL"] {
		t.Fatal("checkbox must directly clear its filter, no Reset button needed")
	}
}

func TestIconOnlyViewControls(t *testing.T) {
	s := New()
	tt := ui.NewTester(catalogTestView(s), 860, 566)
	if err := tt.Click("列表视图"); err != nil {
		t.Fatal(err)
	}
	if s.gridMode {
		t.Fatal("list control did not change view")
	}
	if err := tt.Click("网格视图"); err != nil {
		t.Fatal(err)
	}
	if !s.gridMode {
		t.Fatal("grid control did not change view")
	}
}

func TestProviderSidebarShowsDescriptiveCategoryLabels(t *testing.T) {
	s := New()
	tt := ui.NewTester(catalogTestView(s), 860, 566)
	if !tt.HasText("连接筛选") || !tt.HasText("供应商筛选") {
		t.Fatal("sidebar category headings are not present")
	}
	if err := tt.Click("筛选供应商 华为"); err != nil {
		t.Fatal(err)
	}
	if !s.vendorFilters["华为"] {
		t.Fatal("vendor checkbox not wired")
	}
	if err := tt.Click("筛选供应商 华为"); err != nil {
		t.Fatal(err)
	}
	if len(s.vendorFilters) != 0 {
		t.Fatal("vendor filter did not clear")
	}
}
