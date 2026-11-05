package sqlworkspace

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestSQLCommandLayoutMatrix(t *testing.T) {
	metrics, mode := theme.CurrentMetrics(), theme.CurrentMode()
	t.Cleanup(func() { theme.SetMetrics(metrics); theme.SetMode(mode); i18n.SetLocaleFunc(nil) })
	s, _, tt, _, _ := sqlUITestState(t)
	s.deps.ProfileName = func(int64) string { return strings.Repeat("production_customer_database_", 8) }
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
							tt.SetSize(size[0], size[1])
							tt.SetPreferences(ui.Preferences{TextScale: scale, HighContrast: appearance == "contrast", Accent: ui.Hex("#dd1111")})
							tt.Frame()
							previous := float32(-1)
							for _, label := range []string{"运行当前或选中 SQL", "选择 SQL 执行范围", "取消"} {
								r, ok := tt.Find(locales.Text(label))
								if !ok || r.W <= 0 || r.X < previous || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
									t.Fatalf("primary SQL command hidden or overlapped: %s %+v", label, r)
								}
								previous = r.X + r.W
							}
							if output := os.Getenv("NYADB_UI_OPT_MATRIX"); output != "" {
								if e := os.MkdirAll(output, 0700); e != nil {
									t.Fatal(e)
								}
								f, e := os.Create(filepath.Join(output, "sql-"+name+".png"))
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
						})
					}
				}
			}
		}
	}
}
