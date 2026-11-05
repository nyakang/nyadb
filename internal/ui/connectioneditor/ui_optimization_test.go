package connectioneditor

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestConnectionFieldCaptionFocusAndInlineError(t *testing.T) {
	s := New(Dependencies{})
	if err := s.OpenNew(connections.MySQL, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, 900, 700) }, 900, 700)
	r, ok := tt.Find("主机")
	if !ok {
		t.Fatal("host input missing")
	}
	tt.ClickAt(r.X-12, r.Y+r.H/2)
	if !tt.Focused("主机") {
		t.Fatal("clicking the visible host caption did not focus the input")
	}
	s.connectionDraft.Name = "fixture"
	s.connectionPort = "invalid"
	s.Save()
	tt.Frame()
	if !tt.Focused("端口") || s.validationErrorField != "端口" || !tt.HasText("端口必须是 1–65535 之间的数字") {
		t.Fatal("field validation did not mark and focus the port input")
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("3306")
	if s.validationErrorField != "" {
		t.Fatal("editing did not clear the inline field error")
	}
}

func TestConnectionEditorLayoutMatrix(t *testing.T) {
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
							s := New(Dependencies{})
							if err := s.OpenNew(connections.MySQL, ""); err != nil {
								t.Fatal(err)
							}
							s.connectionDraft.Name = strings.Repeat("production_customer_database_", 8)
							tt := ui.NewTester(func(c *ui.Context) { s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
							tt.SetPreferences(ui.Preferences{TextScale: scale, HighContrast: appearance == "contrast", Accent: ui.Hex("#dd1111")})
							for _, page := range []int{0, 1} {
								s.connectionEditorTab = page
								tt.Frame()
								for _, label := range []string{"保存连接", "取消连接向导", "测试当前 MySQL 配置"} {
									r, ok := tt.Find(locales.Text(label))
									if !ok || r.W <= 0 || r.H < 24 || r.X < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
										t.Fatalf("footer command outside viewport: %s %+v", label, r)
									}
								}
								if output := os.Getenv("NYADB_UI_OPT_MATRIX"); output != "" {
									if err := os.MkdirAll(output, 0700); err != nil {
										t.Fatal(err)
									}
									f, err := os.Create(filepath.Join(output, fmt.Sprintf("editor-%s-page%d.png", name, page)))
									if err != nil {
										t.Fatal(err)
									}
									err = png.Encode(f, tt.Image())
									closeErr := f.Close()
									if err != nil {
										t.Fatal(err)
									}
									if closeErr != nil {
										t.Fatal(closeErr)
									}
								}
							}
							s.Close()
						})
					}
				}
			}
		}
	}
}
