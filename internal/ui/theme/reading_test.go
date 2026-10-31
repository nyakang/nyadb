package theme

import (
	"fmt"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestReadingPreferencesDecodeFieldsAndRoundTrip(t *testing.T) {
	for _, raw := range []string{"", "{", "null", `{"version":2,"ui_font_size":16}`} {
		if DecodeMetrics(raw) != DefaultMetrics() {
			t.Fatalf("bad defaults for %q", raw)
		}
	}
	for _, raw := range []string{
		`{"version":1,"ui_font_size":"bad","sql_font_size":24,"table_density":"relaxed"}`,
		`{"version":1,"ui_font_size":99,"sql_font_size":24,"table_density":"relaxed"}`,
	} {
		if got := DecodeMetrics(raw); got != (MetricsPreferences{12, 24, DensityRelaxed}) {
			t.Fatalf("valid fields lost: %+v", got)
		}
	}
	for _, density := range []TableDensity{DensityCompact, DensityStandard, DensityRelaxed} {
		for _, size := range []int{11, 12, 16} {
			p := MetricsPreferences{size, 24, density}
			if got := DecodeMetrics(EncodeMetrics(p)); got != p {
				t.Fatalf("round trip: %+v", got)
			}
		}
	}
}

func TestReadingMetricsAreFrameLocalAndSQLIsIndependent(t *testing.T) {
	previous := CurrentMetrics()
	t.Cleanup(func() { SetMetrics(previous) })
	SetMetrics(DefaultMetrics())
	var before, after float32
	var current Snapshot
	change := false
	tt := ui.NewTester(func(c *ui.Context) {
		current = Apply(c)
		before = Font(c, 12)
		if change {
			SetMetrics(MetricsPreferences{16, 24, DensityRelaxed})
		}
		after = Font(c, 12)
		ui.Text(c, "sample").FontSize(after)
	}, 160, 80)
	change = true
	tt.Frame()
	if before != 12 || after != 12 {
		t.Fatal("metrics changed within a frame")
	}
	change = false
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	if before != 24 || current.SQLFontSize != 36 || current.Metrics.TableDensity != DensityRelaxed {
		t.Fatalf("scaling compounded or not applied: %+v", current)
	}
	if pure := ResolveScheme(SchemeOcean, ModeLight, false, ui.Preferences{}); pure.Metrics != DefaultMetrics() {
		t.Fatal("pure preview read global metrics")
	}
}

func TestAllThemeReadingSurfacesMeetContrast(t *testing.T) {
	for _, scheme := range Schemes() {
		for _, mode := range []Mode{ModeLight, ModeDark} {
			for _, high := range []bool{false, true} {
				for _, accent := range []ui.Color{ui.Transparent, ui.Hex("#ffff00"), ui.Hex("#777777"), ui.Hex("#dd1111")} {
					s := ResolveScheme(scheme.ID, mode, false, ui.Preferences{HighContrast: high, Accent: accent})
					p := s.Palette
					check := func(label string, ink ui.Color, minimum float64, backgrounds ...ui.Color) {
						t.Helper()
						for _, bg := range backgrounds {
							if r := contrast(ink, bg); r < minimum {
								t.Errorf("%s/%s high=%v accent=%v %s %.2f < %.1f (%v on %v)", scheme.ID, mode, high, accent, label, r, minimum, ink, bg)
							}
						}
					}
					backgrounds := []ui.Color{p.App, p.Header, p.Sidebar, p.Toolbar, p.GridAlternate, p.GridModified, p.Notice}
					check("text", p.Text, 4.5, backgrounds...)
					check("muted", p.Muted, 4.5, backgrounds...)
					check("null", p.GridNull, 4.5, backgrounds...)
					check("selected", p.SelectionText, 4.5, p.Selection)
					check("modified", p.GridModifiedMark, 3, p.GridModified, p.App, p.GridAlternate)
					check("inserted", p.GridInsertedMark, 3, p.App, p.GridAlternate)
					check("deleted", p.GridDeletedMark, 3, p.App, p.GridAlternate)
					check("focus", p.Focus, 3, p.App, p.GridAlternate, p.Header)
					check("SQL", s.SQL.Text, 4.5, sqlBackgrounds(&s.SQL)...)
					for i, token := range s.SQL.Tokens {
						if token.Color.A != 0 {
							check(fmt.Sprintf("SQL token %d", i), token.Color, 4.5, sqlBackgrounds(&s.SQL)...)
						}
					}
					check("SQL caret", s.SQL.Cursor, 3, sqlBackgrounds(&s.SQL)...)
					for _, bg := range sqlBackgrounds(&s.SQL) {
						if bg.A != 255 {
							t.Fatal("unexpected translucent SQL overlap")
						}
					}
					if s.SQL.Match == s.SQL.CurrentMatch || s.SQL.Match == s.SQL.Selection {
						t.Fatal("find and selection states merged")
					}
				}
			}
		}
	}
}

func TestNamedBackgroundsLoseChromaAndKeepLuminance(t *testing.T) {
	for _, scheme := range Schemes()[1:] {
		for _, dark := range []bool{false, true} {
			v := variantFor(scheme.ID, dark)
			p := schemePalette(scheme.ID, dark)
			for _, pair := range [][2]ui.Color{{ui.Hex(v.app), p.App}, {ui.Hex(v.sidebar), p.Sidebar}} {
				spread := func(c ui.Color) uint8 { return max(c.R, c.G, c.B) - min(c.R, c.G, c.B) }
				if spread(pair[1]) >= spread(pair[0]) {
					t.Fatal("chroma was not reduced")
				}
				if math.Abs(luminance(pair[1])-luminance(pair[0])) > 0.004 {
					t.Fatal("desaturation changed brightness")
				}
			}
		}
	}
}
