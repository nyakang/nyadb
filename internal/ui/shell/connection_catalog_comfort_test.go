package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestConnectionChooserCompactRowsRemainIndividuallySelectable(t *testing.T) {
	s := New()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.connectionGridMode = false
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
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
		if s.connectionSelectedName != name {
			t.Fatalf("clicking %s selected %q", name, s.connectionSelectedName)
		}
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
