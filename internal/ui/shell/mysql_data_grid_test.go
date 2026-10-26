package shell

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLGridUsesThemeAndResultOffset(t *testing.T) {
	previous := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(previous) })
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		t.Run(string(mode), func(t *testing.T) {
			theme.SetMode(mode)
			doc := alignedDataTestTab()
			doc.dataSelectedRow = -1
			doc.dataPage.Rows = append(doc.dataPage.Rows, []connections.MySQLDataCell{{Text: "2"}, {Text: ""}}, []connections.MySQLDataCell{{Text: "3"}, {Null: true}})
			s := New()
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.mysqlDataWorkspace(c, doc) }, 540, 320)
			style := doc.dataState.TableStyle
			if style == nil || !style.RowLines || !style.ColumnLines || style.GridColor != theme.Border || style.AlternateRowColor != theme.App.Mix(theme.Header, 0.5) {
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
			doc.dataOffset = 101
			tt.Frame()
			if doc.dataState.TableStyle.RowOffset != 101 {
				t.Fatal("page offset not propagated")
			}
			if got := tt.Image().RGBAAt(2, int(r.Y+r.H/2)); got == want {
				t.Fatal("page parity did not change")
			}
			if !tt.HasText("NULL") {
				t.Fatal("NULL lost its distinct rendering")
			}
			// Empty results keep column headers and remove all data/stripes.
			doc.dataPage.Rows = nil
			tt.Frame()
			if !tt.HasText("label") || tt.HasText("NULL") || tt.HasText("safe-value") {
				t.Fatal("empty result did not retain only headers")
			}
		})
	}
}
