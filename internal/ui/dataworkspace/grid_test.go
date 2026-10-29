package dataworkspace

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLGridUsesThemeAndResultOffset(t *testing.T) {
	previous := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(previous) })
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		t.Run(string(mode), func(t *testing.T) {
			theme.SetMode(mode)
			doc := alignedDataTestTab()
			doc.selectedRow = -1
			doc.page.Rows = append(doc.page.Rows, []mysqlsvc.MySQLDataCell{{Text: "2"}, {Text: ""}}, []mysqlsvc.MySQLDataCell{{Text: "3"}, {Null: true}})
			s := New(Dependencies{})
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 540, 320)
			style := doc.grid.TableStyle
			if style == nil || !style.RowLines || !style.ColumnLines || style.GridColor != theme.PaletteFor(theme.CurrentMode(), true).Border || style.AlternateRowColor != theme.PaletteFor(theme.CurrentMode(), true).App.Mix(theme.PaletteFor(theme.CurrentMode(), true).Header, 0.5) {
				t.Fatalf("incorrect theme grid: %+v", style)
			}
			// Sample the row selector: row backgrounds include its full padding.
			r, ok := tt.Find("2")
			if !ok {
				t.Fatal("second data row missing")
			}
			stripe := style.AlternateRowColor
			want := color.RGBA{R: stripe.R, G: stripe.G, B: stripe.B, A: stripe.A}
			if got := tt.Image().RGBAAt(2, int(r.Y+r.H/2)); got != want {
				t.Fatalf("selector has no stripe: %v != %v", got, want)
			}
			doc.offset = 101
			tt.Frame()
			if doc.grid.TableStyle.RowOffset != 101 {
				t.Fatal("page offset not propagated")
			}
			if got := tt.Image().RGBAAt(2, int(r.Y+r.H/2)); got == want {
				t.Fatal("page parity did not change")
			}
			if !tt.HasText("NULL") {
				t.Fatal("NULL lost its distinct rendering")
			}
			// Empty results keep column headers and remove all data/stripes.
			doc.page.Rows = nil
			tt.Frame()
			if !tt.HasText("label") || tt.HasText("NULL") || tt.HasText("safe-value") {
				t.Fatal("empty result did not retain only headers")
			}
		})
	}
}

func TestSelectedNullCellKeepsReadableInkAfterColumnReorder(t *testing.T) {
	previous := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(previous) })
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		t.Run(string(mode), func(t *testing.T) {
			theme.SetMode(mode)
			doc := alignedDataTestTab()
			doc.selectedRow = -1
			doc.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Null: true}}}
			s := New(Dependencies{})
			var p theme.Palette
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c)
				p = theme.Colors(c)
				s.View(c, doc)
			}, 540, 320)
			assertInk := func(want ui.Color) {
				t.Helper()
				r, ok := tt.Find("NULL")
				if !ok {
					t.Fatal("NULL cell missing")
				}
				pixels := 0
				for y := int(r.Y); y < int(r.Y+r.H); y++ {
					for x := int(r.X); x < int(r.X+r.W); x++ {
						got := tt.Image().RGBAAt(x, y)
						if got.R == want.R && got.G == want.G && got.B == want.B {
							pixels++
						}
					}
				}
				if pixels < 2 {
					t.Fatalf("NULL glyph does not contain expected ink %v (pixels=%d)", want, pixels)
				}
			}
			assertInk(p.GridNull)
			if err := tt.Click("NULL"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !doc.grid.CellSelected(0, 2) {
				t.Fatal("native table did not select NULL")
			}
			assertInk(p.SelectionText)
			before, _ := tt.Find("NULL")
			doc.grid.Columns.Order = []string{"label", "id"}
			tt.Frame()
			// MyGo's CellSelected uses source column indexes after reordering.
			after, _ := tt.Find("NULL")
			if !doc.grid.CellSelected(0, 2) || after.X >= before.X {
				t.Fatal("selection did not follow reordered column")
			}
			assertInk(p.SelectionText)
		})
	}
}
