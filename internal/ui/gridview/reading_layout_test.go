package gridview

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestFitColumnUsesFontMetricsAndBoundedCacheSample(t *testing.T) {
	metrics := theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMetrics(metrics) })
	theme.SetMetrics(theme.DefaultMetrics())
	wide, thin, scaled := float32(0), float32(0), float32(0)
	calls, last := 0, -1
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		wide = FitColumn(c, ui.TableColumn{Title: "W"}, 1, func(int) contract.Cell { return contract.Cell{Kind: contract.Text, Text: "WWWWWWWW"} })
		thin = FitColumn(c, ui.TableColumn{Title: "i"}, 1, func(int) contract.Cell { return contract.Cell{Kind: contract.Text, Text: "iiiiiiii"} })
		scaled = wide
	}, 200, 80)
	if wide <= thin {
		t.Fatalf("proportional glyphs were not measured: %g %g", wide, thin)
	}
	before := wide
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	if scaled <= before {
		t.Fatal("text scaling was ignored")
	}
	ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		calls = 0
		last = -1
		FitColumn(c, ui.TableColumn{}, 10000, func(row int) contract.Cell { calls++; last = row; return contract.Cell{Kind: contract.Text, Text: "x"} })
	}, 200, 80)
	if calls != 200 || last != 9999 {
		t.Fatalf("unbounded or nonuniform sampling: calls=%d last=%d", calls, last)
	}
	var capped float32
	ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		calls = 0
		capped = FitColumn(c, ui.TableColumn{}, 10000, func(int) contract.Cell {
			calls++
			return contract.Cell{Kind: contract.Text, Text: strings.Repeat("W", 2000)}
		})
	}, 200, 80)
	if capped != 800 || calls != 1 {
		t.Fatalf("long cached value exceeded budget: %g %d", capped, calls)
	}
}
