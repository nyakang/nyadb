package dataworkspace

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLNativeColumnContextActionsTranslatedInEnglish(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })
	s := New(Dependencies{})
	doc := alignedDataTestTab()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, doc)
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
