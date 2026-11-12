package kvworkspace

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

func assertHTTPSessionRenewalControls(t *testing.T, tt *ui.Tester, size [2]int, labels ...string) {
	t.Helper()
	for _, label := range labels {
		r, ok := tt.Find(locales.Text(label))
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("HTTP Session control clipped", label, size, r)
		}
	}
}
func captureHTTPSessionRenewal(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	folder := os.Getenv("NYADB_CONSUL_SESSION_RENEWAL_IMAGES")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-workspace-session-renewal-%s.png", language, size[0], size[1], stage)))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	ce := f.Close()
	if err != nil || ce != nil {
		t.Fatal(err, ce)
	}
}
func TestHTTPSessionRenewalUIBilingualFormConfirmationStopAndReceipt(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", lang, size[0], size[1]), func(t *testing.T) {
				s, d, p := httpSessionRenewalTest(t)
				p.lease.run.batch.Observations = append(p.lease.run.batch.Observations, renewalObservation("GET", "original native body", 0))
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "Session UUID", "续租间隔", "最多续租次数", "记录预算（字节）", "预览 HTTP Session 续租")
				captureHTTPSessionRenewal(t, tt, lang, size, "unit-form")
				original := d.Request()
				workspaceClick(t, tt, "续租 JSON")
				assertHTTPSessionRenewalControls(t, tt, size, "KV JSON 编辑器")
				workspaceClick(t, tt, "续租表单")
				if d.Request() != original {
					t.Fatal("form/JSON switch rewrote request")
				}
				workspaceClick(t, tt, "预览 HTTP Session 续租")
				assertHTTPSessionRenewalControls(t, tt, size, "确认 HTTP Session 续租", "我授权按以上 UUID、间隔、次数和预算续租", "确认并开始续租", "复制完整请求", "取消")
				captureHTTPSessionRenewal(t, tt, lang, size, "unit-confirm")
				workspaceClick(t, tt, "确认并开始续租")
				if d.Busy() || p.lease.starts.Load() != 0 {
					t.Fatal("unchecked UI executed")
				}
				workspaceClick(t, tt, "我授权按以上 UUID、间隔、次数和预算续租")
				workspaceClick(t, tt, "确认并开始续租")
				eventEventually(t, func() bool { tt.Frame(); return d.HTTPSessionRenewalStatus().Observations == 1 })
				workspaceClick(t, tt, "停止 HTTP Session 续租")
				if !d.Busy() || !d.CancelRequested() {
					t.Fatal("UI stop inferred ACK")
				}
				close(p.lease.run.ack)
				consume(t, s, d)
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "复制完整续租记录", "导出续租记录", "原 HTTP Session 续租已停止")
				captureHTTPSessionRenewal(t, tt, lang, size, "unit-stopped")
				workspaceClick(t, tt, "复制完整续租记录")
				consume(t, s, d)
				if !strings.Contains(tt.Clipboard(), `"lock_delay":"18446744073709551615"`) || !strings.Contains(tt.Clipboard(), `"session_id":"`+renewalUUID+`"`) {
					t.Fatal("UI clipboard incomplete")
				}
				workspaceClick(t, tt, "最近回执")
				if d.selectedHTTPSessionRenewal {
					t.Fatal("receipt tab failed to switch")
				}
				workspaceClick(t, tt, "HTTP Session 续租记录")
				if !d.selectedHTTPSessionRenewal {
					t.Fatal("original receipt inaccessible")
				}
				workspaceClick(t, tt, "清除原续租记录")
				if d.httpSessionRenewalContent() {
					t.Fatal("UI clear failed")
				}
			})
		}
	}
}
