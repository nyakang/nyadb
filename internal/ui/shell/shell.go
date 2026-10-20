// Package shell implements the native NyaDB M0 workbench.
package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// State contains persistent application and widget state; ui.Element values
// must never be stored here because they live for one view-build pass.
type State struct {
	sidebarWidth  float32
	editorPercent float32
	activeTab     int
	demoRuns      int
	status        string

	tree        ui.OutlineState[string]
	treeRow     int
	resultTable ui.ListState
	sql         *editor.State
	columns     []ui.TableColumn
}

const (
	// Synthetic preview dimensions; the application never allocates every row.
	PreviewRows    = 100_000
	PreviewColumns = 50
)

func New() *State {
	buf := editor.NewBuffer("SELECT id, name, email FROM customers ORDER BY id;\n")
	buf.SetLanguage(editor.LanguageNamed("SQL"))
	columns := make([]ui.TableColumn, PreviewColumns)
	columns[0] = ui.TableColumn{Title: "id", Width: 90, Fixed: true}
	columns[1] = ui.TableColumn{Title: "name", Width: 180}
	columns[2] = ui.TableColumn{Title: "email", Width: 240}
	for i := 3; i < len(columns); i++ {
		columns[i] = ui.TableColumn{Title: fmt.Sprintf("field_%02d", i-2), Width: 150}
	}
	return &State{
		sidebarWidth:  238,
		editorPercent: 46,
		status:        "Demo mode — no database connection",
		sql:           &editor.State{Buffer: buf, WrapGuides: []int{100}},
		columns:       columns,
	}
}

func (s *State) View(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(10, 14).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "NyaDB").Bold().FontSize(19)
			ui.Text(c, "Database Workbench").TextColor(t.TextMuted).FontSize(12)
			ui.Box(c).Grow(1)
			ui.Text(c, "M0 • Native UI prototype").TextColor(t.TextMuted).FontSize(12)
		})
		ui.Split(c, &s.sidebarWidth,
			func() { s.sidebar(c) },
			func() { s.workspace(c) },
		).Grow(1)
		ui.Row(c).AlignItems(ui.Center).Gap(12).Padding(6, 12).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, s.status).FontSize(12).TextColor(t.TextMuted)
			ui.Box(c).Grow(1)
			ui.Text(c, "100,000 synthetic rows • 50 columns").FontSize(12).TextColor(t.TextMuted)
		})
	})
}

func (s *State) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(10).Background(t.Surface).Children(func() {
		ui.Text(c, "EXPLORER").Bold().FontSize(12).TextColor(t.TextMuted)
		s.tree.List.Selected = &s.treeRow
		ui.Outline(c, &s.tree, []string{"Demo connection"}, demoChildren, func(node string) {
			ui.Text(c, node).SingleLine()
		}).Grow(1)
		ui.Text(c, "Mock schema • M2 adds real connections").FontSize(11).TextColor(t.TextMuted)
	})
}

func demoChildren(node string) []string {
	switch node {
	case "Demo connection":
		return []string{"demo_db"}
	case "demo_db":
		return []string{"Tables", "Views"}
	case "Tables":
		return []string{"customers", "orders"}
	case "Views":
		return []string{"active_customers"}
	default:
		return nil
	}
}

func (s *State) workspace(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		ui.Tabs(c, &s.activeTab, "SQL Editor", "Data Preview")
		if s.activeTab == 1 {
			s.results(c)
			return
		}
		ui.SplitVerticalPercent(c, &s.editorPercent,
			func() { s.sqlEditor(c) },
			func() { s.results(c) },
		).Grow(1)
	})
}

func (s *State) sqlEditor(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(8, 12).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "query.sql").TextColor(t.TextMuted)
			ui.Box(c).Grow(1)
			if ui.PrimaryButton(c, "Run demo").Clicked() {
				s.demoRuns++
				s.status = fmt.Sprintf("Demo query executed (%d) — no database accessed", s.demoRuns)
			}
		})
		editor.View(c, s.sql).Grow(1).FillWidth()
	})
}

func (s *State) results(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(8, 12).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "RESULTS").Bold().FontSize(12)
			ui.Text(c, "Synthetic dataset — not SQL output").FontSize(12).TextColor(t.TextMuted)
		})
		ui.Table(c, &s.resultTable, s.columns, PreviewRows, func(row, col int) {
			ui.Text(c, PreviewCell(row, col)).SingleLine()
		}).Grow(1)
	})
}

// PreviewCell generates deterministic test data on demand. Memory use does
// not grow with PreviewRows; actual database paging arrives in M5.
func PreviewCell(row, col int) string {
	switch col {
	case 0:
		return fmt.Sprint(row + 1)
	case 1:
		return fmt.Sprintf("customer_%06d", row+1)
	case 2:
		return fmt.Sprintf("user%06d@example.test", row+1)
	default:
		return fmt.Sprintf("value_%d_%d", row+1, col)
	}
}
