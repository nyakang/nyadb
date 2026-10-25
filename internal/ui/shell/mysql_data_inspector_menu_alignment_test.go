package shell

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLInspectorQualifiedFieldHeaderFitsNarrowWidth(t *testing.T) {
	doc := mysqlInspectorSampleTab()
	for _, width := range []int{180, 205} {
		tt := ui.NewTester(func(c *ui.Context) {
			theme.Apply(c)
			(&State{}).mysqlDataInspector(c, doc)
		}, width, 480)
		for _, label := range []string{"id", "test_schema.entries", "bigint unsigned", "auto_increment"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(width) {
				t.Fatalf("metadata %q clipped beyond %dpx inspector: %+v found=%v", label, width, r, ok)
			}
		}
	}
}

func TestMySQLNativeColumnContextActionsTranslatedInEnglish(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })
	s := New()
	doc := alignedDataTestTab()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.mysqlDataWorkspace(c, doc)
	}, 900, 600)
	if err := tt.RightClick("label"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	for _, label := range []string{"Copy Field Name", "Copy Column (Current Page)",
		"Hide Column", "Show/Hide Columns", "Show All Columns",
		"Fit Column Width to Current Page", "Reset All Column Widths", "Show Field Types"} {
		if !slices.Contains(menu, label) {
			t.Fatalf("missing translated native column menu item %q: %v", label, menu)
		}
	}
	if err := tt.ChooseMenuItem("Copy Field Name"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "label" {
		t.Fatalf("translated menu copied wrong field: %q", tt.Clipboard())
	}
}
