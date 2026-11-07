package shell

import (
	"os"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func TestDamengChooserOpensIndependentNativeEditor(t *testing.T) {
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
			s := New()
			t.Cleanup(s.CloseMySQLSessions)
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
			if err := tt.Click(locales.Text("选择 ") + "达梦"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(locales.Text("下一步")); err != nil {
				t.Fatal(err)
			}
			if s.connectionStep != 1 || s.connectionEditor.Draft().Kind != connections.Dameng || s.ConnectionWindowTitle() != locales.Text("新建连接")+" (达梦)" {
				t.Fatal("chooser lost Dameng identity")
			}
			icon, err := s.ConnectionWindowIcon()
			if err != nil || len(icon) == 0 {
				t.Fatal("Dameng native window missing brand icon")
			}
			if !tt.HasText(locales.Text("测试连接")) {
				t.Fatal("Dameng test button missing")
			}
			if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
				if err := os.MkdirAll(output, 0755); err != nil {
					t.Fatal(err)
				}
				writeDomainCapture(t, output, language+"-dameng-editor", tt)
			}
			for _, page := range []string{"高级", "SSL"} {
				if err := tt.Click(locales.Text(page)); err != nil {
					t.Fatal(err)
				}
				if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
					writeDomainCapture(t, output, language+"-dameng-"+page, tt)
				}
			}
			tt = ui.NewTester(s.ConnectionWindowView, 840, 560)
			tt.SetPreferences(ui.Preferences{TextScale: 1.5})
			if err := tt.Click(locales.Text("高级")); err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{"保存连接", "取消连接向导", "测试连接"} {
				r, ok := tt.Find(locales.Text(label))
				if !ok || r.W <= 0 || r.X < 0 || r.X+r.W > 841 || r.Y+r.H > 561 {
					t.Fatalf("compact native control outside viewport: %s %+v", label, r)
				}
			}
			if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
				writeDomainCapture(t, output, language+"-dameng-advanced-compact", tt)
			}
		})
	}
}
