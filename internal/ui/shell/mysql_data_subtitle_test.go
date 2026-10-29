package shell

import (
	"fmt"
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestPatchedNativeMyGoTableSubtitleSharesColumnBehaviors(t *testing.T) {
	var order ui.SortOrder
	var selected int
	var colMenu int = -2
	state := ui.ListState{Sort: &order, Selected: &selected}
	state.HeaderMenu = func(m *ui.Menu, index int) {
		colMenu = index
		m.Item("检查表头").Chosen()
	}
	columns := []ui.TableColumn{
		{ID: "a", Title: "alpha_field", Subtitle: "INTEGER", Width: 170, MinWidth: 75, Sortable: true},
		{ID: "b", Title: "beta_field", Subtitle: "VARCHAR", Width: 170, MinWidth: 75, Sortable: true},
		{ID: "c", Title: "gamma_field", Subtitle: "DECIMAL", Width: 170, MinWidth: 75, Sortable: true},
	}
	render := func(c *ui.Context) {
		ui.Column(c).Width(410).Height(230).Children(func() {
			ui.Table(c, &state, columns, 8, func(row, col int) {
				ui.Text(c, fmt.Sprintf("value_%d_%d", row, col)).SingleLine()
			}).Grow(1)
		})
	}
	tt := ui.NewTester(render, 480, 280)
	tt.Frame()
	if path := os.Getenv("NYADB_MYGO_SUBTITLE_CAPTURE"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	title, ok := tt.Find("alpha_field")
	if !ok {
		t.Fatalf("missing title: %v", tt.Texts())
	}
	sub, ok := tt.Find("INTEGER")
	if !ok {
		t.Fatalf("missing subtitle: %v", tt.Texts())
	}
	if sub.Y <= title.Y || sub.X < title.X-4 || sub.X > title.X+12 {
		t.Fatalf("subtitle isn't inside the same native column: title=%+v sub=%+v", title, sub)
	}
	if err := tt.Click("INTEGER"); err != nil {
		t.Fatal(err)
	}
	if order.Column != "a" || order.Descending {
		t.Fatalf("subtitle click did not sort column: %+v", order)
	}
	if err := tt.RightClick("INTEGER"); err != nil {
		t.Fatal(err)
	}
	if colMenu != 0 {
		t.Fatalf("subtitle right-click targeted column %d", colMenu)
	}
	if err := tt.ChooseMenuItem("检查表头"); err != nil {
		t.Fatal(err)
	}
	// Resize using the actual header's right edge; widths are shared with
	// the row cells because TableLayout is the only source of their widths.
	edge := float32(166)
	y := float32(sub.Y + sub.H/2)
	t.Logf("header title=%+v subtitle=%+v edge=%v y=%v", title, sub, edge, y)
	tt.Press(edge, y)
	tt.Move(edge+38, y)
	tt.Release(edge+38, y)
	tt.Frame()
	if state.Columns.Widths["a"] <= 170 {
		t.Fatalf("subtitle header resize didn't persist shared width: %+v", state.Columns.Widths)
	}
	before, ok := tt.Find("beta_field")
	if !ok {
		t.Fatal("second column vanished after resize")
	}
	cellBefore, ok := tt.Find("value_0_1")
	if !ok {
		t.Fatal("second column cell missing")
	}
	tt.Scroll(200, 140, 220, 0)
	tt.Frame()
	after, ok := tt.Find("beta_field")
	if !ok {
		t.Fatal("second header vanished after horizontal scroll")
	}
	cellAfter, ok := tt.Find("value_0_1")
	if !ok {
		t.Fatal("second cell vanished after horizontal scroll")
	}
	headShift := before.X - after.X
	cellShift := cellBefore.X - cellAfter.X
	if headShift <= 0 || headShift < cellShift-2 || headShift > cellShift+2 {
		t.Fatalf("native header and data horizontal scroll diverged: header=%v cell=%v", headShift, cellShift)
	}
	tt.RightClick("VARCHAR")
	if colMenu != 1 {
		t.Fatalf("menu lost source index after horizontal scroll: %d", colMenu)
	}
	tt.CloseMenu()
	// Drag beta past gamma. The row and subtitle move together with the
	// original header node; sorting still points to stable column ID "a".
	tt.Scroll(200, 140, -300, 0)
	tt.Frame()
	beta, ok := tt.Find("beta_field")
	if !ok {
		t.Fatal("beta column missing before reorder")
	}
	x := float32(beta.X + 20)
	y = float32(beta.Y + beta.H/2)
	tt.Press(x, y)
	for _, d := range []float32{5, 20, 50, 80, 110, 145} {
		tt.Move(x+d, y)
	}
	tt.Release(x+145, y)
	tt.Frame()
	if len(state.Columns.Order) != 3 || state.Columns.Order[1] != "c" || state.Columns.Order[2] != "b" {
		t.Fatalf("subtitle column was not reordered with the native header: %+v", state.Columns.Order)
	}
	if order.Column != "a" {
		t.Fatalf("drag changed active sort: %+v", order)
	}
	// Empty subtitles retain the original compact native header layout.
	for i := range columns {
		columns[i].Subtitle = ""
	}
	tt.Frame()
	single, ok := tt.Find("alpha_field")
	if !ok || single.H >= title.H || tt.HasText("INTEGER") {
		t.Fatalf("single-line backwards compatibility: old=%+v now=%+v", title, single)
	}
}

func TestMySQLFullServerTypesAndKeyBadgesUseNativeTable(t *testing.T) {
	page := &mysqlsvc.MySQLDataPage{
		Columns:     []string{"id", "status", "price"},
		ColumnKinds: []string{"bigint", "enum", "decimal"},
		ColumnMetadata: []mysqlsvc.MySQLStructureColumn{
			{Name: "id", DataType: "bigint", Type: "bigint unsigned", Key: "PRI"},
			{Name: "status", DataType: "enum", Type: "enum('active','disabled')", Key: "MUL"},
			{Name: "price", DataType: "decimal", Type: "decimal(10,2)"},
		},
	}
	tab := &tab{dataShowFieldTypes: true, dataColumns: []ui.TableColumn{
		{Title: "id", ID: "id", Width: 155}, {Title: "status", ID: "status", Width: 220}, {Title: "price", ID: "price", Width: 155},
	}}
	ids, cols := mysqlVisibleDataColumns(tab, page)
	if len(ids) != 3 || cols[0].Subtitle != "bigint unsigned" || cols[0].HeaderBadge != "PK" ||
		cols[0].SubtitleIcon != "#" || cols[1].Subtitle != "enum('active','disabled')" ||
		cols[1].HeaderBadge != "IDX" || cols[1].SubtitleIcon != "A" ||
		cols[2].Subtitle != "decimal(10,2)" || cols[2].ID != "price" {
		t.Fatalf("real declaration/key glyph mapping lost: %+v", cols)
	}
	state := ui.ListState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Table(c, &state, cols, 1, func(row, col int) {
			ui.Text(c, "cell").SingleLine()
		}).Width(480).Height(160)
	}, 500, 200)
	tt.Frame()
	if !tt.HasText("enum('active','disabled')") || !tt.HasText("decimal(10,2)") || !tt.HasText("PK") {
		t.Fatalf("native table did not render complete field declarations: %v", tt.Texts())
	}
	tab.dataShowFieldTypes = false
	_, hidden := mysqlVisibleDataColumns(tab, page)
	if hidden[0].Subtitle != "" || hidden[0].SubtitleIcon != "" || hidden[0].HeaderBadge != "" ||
		hidden[1].ID != cols[1].ID {
		t.Fatalf("hidden subtitles did not preserve stable identities: %+v", hidden)
	}
	if mysqlColumnKeyBadge("MUL") != "IDX" || mysqlColumnKeyBadge("") != "" ||
		mysqlColumnTypeGlyph("json") != "{}" || mysqlColumnTypeGlyph("datetime") == "#" {
		t.Fatal("type glyphs or index markers are misleading")
	}
}
