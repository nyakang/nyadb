package dataworkspace

import (
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDraftAppearanceFollowsSourceColumnAndSelection(t *testing.T) {
	mode, scheme, metrics := theme.CurrentMode(), theme.CurrentScheme(), theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme); theme.SetMetrics(metrics) })
	for _, item := range theme.Schemes() {
		for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark} {
			theme.SetScheme(item.ID)
			theme.SetMode(mode)
			s, doc, tt, f := editingTestWorkspace(t)
			doc.edit.Batch = true
			s.setMySQLDataCell(ui.Services{}, doc, 0, 1, mysqlsvc.MySQLDataValue{Text: "modified"})
			tt.Frame()
			p := theme.ResolveScheme(item.ID, mode, false, ui.Preferences{}).Palette
			assertBackground := func(source int, want ui.Color) {
				t.Helper()
				r := editingCellBounds(t, doc, 0, source)
				got := tt.Image().RGBAAt(int(r.X+r.W-9), int(r.Y+r.H/2))
				if got.R != want.R || got.G != want.G || got.B != want.B {
					t.Fatalf("%s %s col=%d got=%v want=%v", item.ID, mode, source, got, want)
				}
			}
			assertBackground(1, p.GridModified)
			// A background can pass pixel checks while covering the text itself.
			textRect, ok := tt.Find("modified")
			if !ok {
				t.Fatal("draft text missing")
			}
			pixels := 0
			frame := tt.Image()
			for y := int(textRect.Y); y < int(textRect.Y+textRect.H); y++ {
				for x := int(textRect.X); x < int(textRect.X+textRect.W); x++ {
					got := frame.RGBAAt(x, y)
					if got.R == p.Text.R && got.G == p.Text.G && got.B == p.Text.B {
						pixels++
					}
				}
			}
			if pixels < 2 {
				t.Fatal("draft background obscured the edited value")
			}

			assertBackground(2, p.App)
			doc.grid.Columns.Order = []string{"extra", "note", "id"}
			doc.hiddenColumns["id"] = true
			tt.Frame()
			assertBackground(1, p.GridModified)
			if err := tt.Click("modified"); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyEnter)
			tt.Frame()
			assertBackground(1, p.Selection)
			if !mysqlDataCellModified(doc, 0, 1) || mysqlDataCellModified(doc, 0, 2) || f.writeCount() != 0 {
				t.Fatal("decorating modified an unrelated cell or wrote data")
			}
		}
	}
}

func TestReadingDensityKeepsCellEditorAndDrafts(t *testing.T) {
	metrics := theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMetrics(metrics) })
	theme.SetMetrics(theme.DefaultMetrics())
	s, doc, tt, f := editingTestWorkspace(t)
	doc.edit.Batch = true
	doc.edit.Zoom = 150
	s.setMySQLDataCell(ui.Services{}, doc, 0, 1, mysqlsvc.MySQLDataValue{Text: "modified"})
	tt.Frame()
	before := mysqlDataLayoutSnapshot(doc)
	drafts := cloneMySQLDataDraftsForAppearance(doc)
	var previousHeight float32
	for _, density := range []theme.TableDensity{theme.DensityCompact, theme.DensityStandard, theme.DensityRelaxed} {
		theme.SetMetrics(theme.MetricsPreferences{UIFontSize: 16, SQLFontSize: 24, TableDensity: density})
		tt.Frame()
		tt.Frame()
		cell := editingCellBounds(t, doc, 1, 1)
		if cell.H <= previousHeight {
			t.Fatalf("density did not add row space: %v -> %v", previousHeight, cell.H)
		}
		previousHeight = cell.H
		tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
		tt.Frame()
		input, ok := tt.Find("单元格编辑 note")
		if !ok || math.Abs(float64(input.X-cell.X))+math.Abs(float64(input.Y-cell.Y))+math.Abs(float64(input.W-cell.W))+math.Abs(float64(input.H-cell.H)) > 0.01 {
			t.Fatalf("density changed editor bounds: %v %v", cell, input)
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if !reflect.DeepEqual(drafts, cloneMySQLDataDraftsForAppearance(doc)) || mysqlDataLayoutSnapshot(doc) != before || f.writeCount() != 0 {
			t.Fatal("appearance changed drafts, layout or database")
		}
	}
}

func cloneMySQLDataDraftsForAppearance(doc *Document) map[string]map[string]mysqlsvc.MySQLDataValue {
	result := map[string]map[string]mysqlsvc.MySQLDataValue{}
	for key, draft := range doc.edit.Drafts {
		values := map[string]mysqlsvc.MySQLDataValue{}
		for k, v := range draft.Values {
			values[k] = v
		}
		result[key] = values
	}
	return result
}

func TestDataAppearanceVisualMatrix(t *testing.T) {
	output := os.Getenv("NYADB_THEME_CAPTURE")
	if output == "" {
		t.Skip("set NYADB_THEME_CAPTURE for data appearance screenshots")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	mode, scheme, metrics := theme.CurrentMode(), theme.CurrentScheme(), theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme); theme.SetMetrics(metrics) })
	for _, item := range theme.Schemes() {
		for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark} {
			theme.SetScheme(item.ID)
			theme.SetMode(mode)
			theme.SetMetrics(theme.DefaultMetrics())
			s, doc, tt, _ := editingTestWorkspace(t)
			doc.edit.Batch = true
			doc.layout.showRowNumbers = true
			s.setMySQLDataCell(ui.Services{}, doc, 0, 1, mysqlsvc.MySQLDataValue{Text: "待保存 / unsaved"})
			doc.page.Rows[2][2].Text = "NULL"
			tt.SetSize(840, 560)
			tt.Frame()
			tt.Click("NULL")
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			name := string(item.ID) + "-" + string(mode) + "-data.png"
			file, err := os.Create(filepath.Join(output, name))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
	}
}
