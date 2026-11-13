package kvworkspace

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

func captureSessionDestroy(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_SESSION_DESTROY_IMAGES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-%s.png", language, size[0], size[1], stage)))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestHTTPSessionDestroyBilingualImmutableConfirmationPagesAndCancel(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l := destroyTestState(t)
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "Session 列表")
				workspaceClick(t, tt, sessionKeysTestID)
				assertHTTPSessionRenewalControls(t, tt, size, "预览销毁所选 Session")
				workspaceClick(t, tt, "预览销毁所选 Session")
				consume(t, s, d)
				tt.Frame()
				if d.confirmation == nil {
					t.Fatal(d.Error())
				}
				assertHTTPSessionRenewalControls(t, tt, size, "确认销毁原 Session", "复制完整 Session 销毁预览", "我确认销毁此原 Session", "确认销毁 Session", "原 Session 持有键与版本")
				captureSessionDestroy(t, tt, language, size, "local-confirm")
				workspaceClick(t, tt, "下一页")
				if d.confirmation.destroy.page != 1 {
					t.Fatal("confirmation paging did not preserve original keys")
				}
				captureSessionDestroy(t, tt, language, size, "local-confirm-last-page")
				workspaceClick(t, tt, "复制完整 Session 销毁预览")
				if tt.Clipboard() == "" {
					t.Fatal("complete preview copy missing")
				}
				workspaceClick(t, tt, "取消")
				if !l.plan.closed.Load() || d.confirmation != nil || l.executes.Load() != 0 {
					t.Fatal("cancelled UI plan retained authority")
				}
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "Session 销毁原始记录", "选择原始步骤回执", "KV 原生回执")
				captureSessionDestroy(t, tt, language, size, "local-preview-record")
			})
		}
	}
}
