package connectioncatalog

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestChooserLayoutMatrix(t *testing.T) {
	metrics, mode := theme.CurrentMetrics(), theme.CurrentMode()
	t.Cleanup(func() { theme.SetMetrics(metrics); theme.SetMode(mode); i18n.SetLocaleFunc(nil) })
	for _, locale := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return locale })
		for _, font := range []int{12, 16} {
			m := theme.DefaultMetrics()
			m.UIFontSize = font
			theme.SetMetrics(m)
			for _, scale := range []float32{1, 1.5} {
				for _, size := range [][2]int{{1280, 800}, {840, 560}} {
					for _, appearance := range []string{"light", "dark", "contrast"} {
						theme.SetMode(theme.ModeLight)
						if appearance != "light" {
							theme.SetMode(theme.ModeDark)
						}
						name := fmt.Sprintf("%s-font%d-scale%.1f-%dx%d-%s", locale, font, scale, size[0], size[1], appearance)
						t.Run(name, func(t *testing.T) {
							s := New()
							tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1]), nil) }, size[0], size[1])
							tt.SetPreferences(ui.Preferences{TextScale: scale, HighContrast: appearance == "contrast", Accent: ui.Hex("#dd1111")})
							for _, grid := range []bool{true, false} {
								s.gridMode = grid
								tt.Frame()
								labels := []string{"MySQL", "PostgreSQL", "SQLite"}
								rects := []ui.Rect{}
								for _, label := range labels {
									r, ok := tt.Find(locales.Text("选择 ") + label)
									if !ok {
										continue
									} // Native virtualization only publishes visible cards/rows.
									if r.Y+r.H >= float32(size[1])-1 && r.H < float32(94)*float32(font)/12*scale {
										continue
									} // A partially clipped final row/card is revealed by scrolling.
									minimum := float32(19)
									if grid {
										minimum = 94
									}
									if r.W <= 0 || r.H+1 < minimum*float32(font)/12*scale || r.X < 0 || r.X+r.W > float32(size[0])+1 {
										t.Fatalf("invalid chooser item %s: %+v", label, r)
									}
									for _, prev := range rects {
										if r.X < prev.X+prev.W && r.X+r.W > prev.X && r.Y < prev.Y+prev.H && r.Y+r.H > prev.Y {
											t.Fatalf("chooser items overlap: %+v %+v", prev, r)
										}
									}
									rects = append(rects, r)
								}
								if len(rects) == 0 {
									t.Fatal("no visible chooser items")
								}
								if output := os.Getenv("NYADB_UI_OPT_MATRIX"); output != "" {
									if e := os.MkdirAll(output, 0700); e != nil {
										t.Fatal(e)
									}
									f, e := os.Create(filepath.Join(output, fmt.Sprintf("chooser-%s-grid%t.png", name, grid)))
									if e != nil {
										t.Fatal(e)
									}
									e = png.Encode(f, tt.Image())
									closeErr := f.Close()
									if e != nil {
										t.Fatal(e)
									}
									if closeErr != nil {
										t.Fatal(closeErr)
									}
								}
							}
						})
					}
				}
			}
		}
	}
}
