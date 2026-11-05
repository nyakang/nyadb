package objectworkspace

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"testing"
)

func TestAccountModalLargeTextKeepsFooterVisible(t *testing.T) {
	metrics := theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMetrics(metrics); i18n.SetLocaleFunc(nil) })
	m := theme.DefaultMetrics()
	m.UIFontSize = 16
	theme.SetMetrics(m)
	for _, locale := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return locale })
		s := New(Dependencies{})
		s.accounts = &accountPanel{open: true, user: "fixture", host: "localhost", action: "grant"}
		tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.accountModal(c) }, 840, 560)
		tt.SetPreferences(ui.Preferences{TextScale: 1.5})
		for _, label := range []string{"关闭", "读取账户与授权", "确认执行账户操作"} {
			r, ok := tt.Find(locales.Text(label))
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 20 || r.Y < 20 || r.X+r.W > 820 || r.Y+r.H > 540 {
				t.Fatalf("account footer clips %s: %+v", label, r)
			}
		}
		s.CloseAll()
	}
}
