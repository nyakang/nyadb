package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestInitialNativeView(t *testing.T) {
	state := New()
	tt := ui.NewTester(state.View, 1280, 800)
	if !tt.HasText("NyaDB") {
		t.Fatalf("missing title, got %q", tt.Texts())
	}
	if !tt.HasText("SQL Editor") {
		t.Fatalf("missing SQL Editor tab, got %q", tt.Texts())
	}
	if state.sql.Buffer.Language() == nil || state.sql.Buffer.Language().Name != "SQL" {
		t.Fatal("expected SQL syntax highlighting language")
	}
}

func TestRunDemoUpdatesState(t *testing.T) {
	state := New()
	tt := ui.NewTester(state.View, 1280, 800)
	if err := tt.Click("Run demo"); err != nil {
		t.Fatal(err)
	}
	if state.demoRuns != 1 || !strings.Contains(state.status, "Demo query executed (1)") {
		t.Fatalf("unexpected demo status: runs=%d status=%q", state.demoRuns, state.status)
	}
}

func TestSwitchWorkspaceTabs(t *testing.T) {
	state := New()
	tt := ui.NewTester(state.View, 1280, 800)
	if err := tt.Click("Data Preview"); err != nil {
		t.Fatal(err)
	}
	if state.activeTab != 1 || !tt.HasText("Synthetic dataset — not SQL output") {
		t.Fatalf("preview was not selected: tab=%d", state.activeTab)
	}
	if err := tt.Click("SQL Editor"); err != nil {
		t.Fatal(err)
	}
	if state.activeTab != 0 || !tt.HasText("query.sql") {
		t.Fatalf("editor was not restored: tab=%d", state.activeTab)
	}
}

func TestSyntheticPreview(t *testing.T) {
	if PreviewColumns != 50 || PreviewRows != 100_000 {
		t.Fatal("unexpected fixture dimensions")
	}
	if got := PreviewCell(9, 0); got != "10" {
		t.Fatalf("row ID: %q", got)
	}
	if got := PreviewCell(9, 1); got != "customer_000010" {
		t.Fatalf("name: %q", got)
	}
	if got := PreviewCell(9, 2); got != "user000010@example.test" {
		t.Fatalf("email: %q", got)
	}
	if got := demoChildren("customers"); got != nil {
		t.Fatalf("table should be a leaf, got %v", got)
	}
}
