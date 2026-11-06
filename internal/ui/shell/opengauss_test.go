package shell

import (
	"os"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func TestOpenGaussChooserOpensIndependentNativeEditor(t *testing.T) {
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
			s := New()
			t.Cleanup(s.CloseMySQLSessions)
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
			if err := tt.Click(locales.Text("选择 ") + "openGauss"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(locales.Text("下一步")); err != nil {
				t.Fatal(err)
			}
			if s.connectionStep != 1 || s.connectionEditor.Draft().Kind != connections.OpenGauss || s.ConnectionWindowTitle() != locales.Text("新建连接")+" (openGauss)" {
				t.Fatal("chooser lost openGauss identity")
			}
			icon, err := s.ConnectionWindowIcon()
			if err != nil || len(icon) == 0 {
				t.Fatal("openGauss native window missing brand icon")
			}
			if !tt.HasText(locales.Text("测试连接")) {
				t.Fatal("openGauss test button missing")
			}
			if output := os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"); output != "" {
				if err := os.MkdirAll(output, 0755); err != nil {
					t.Fatal(err)
				}
				writeDomainCapture(t, output, language+"-opengauss-editor", tt)
			}
		})
	}
}
