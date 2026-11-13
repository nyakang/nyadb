package kvworkspace

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

func captureSessionHeldKeys(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_SESSION_HELD_KEYS_IMAGES")
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

func TestHTTPSessionHeldKeysBilingualFormsPagesDetailsAndFilteredBoundary(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l := sessionKeysTestState(t)
				original := d.Request()
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "Session 列表")
				workspaceClick(t, tt, sessionKeysTestID)
				assertHTTPSessionRenewalControls(t, tt, size, "读取所选 Session 的持有键（全根目录）")
				captureSessionHeldKeys(t, tt, language, size, "selected-session")
				workspaceClick(t, tt, "读取所选 Session 的持有键（全根目录）")
				consume(t, s, d)
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "返回原 HTTP 回执", "筛选已返回的持有键", "下一页", "查看所选原始详情")
				body := bytes.Clone(d.last.Result.HTTP.Body)
				captureSessionHeldKeys(t, tt, language, size, "held-keys")
				workspaceClick(t, tt, "下一页")
				workspaceClick(t, tt, "outside/中文 052")
				workspaceClick(t, tt, "复制所选持有键名称")
				if tt.Clipboard() != "outside/中文 052" {
					t.Fatal("native clipboard changed original last-page key")
				}
				workspaceClick(t, tt, "查看所选原始详情")
				if !strings.Contains(d.httpSessionKeysBrowser.preview, "18446744073709551615") || !strings.Contains(d.httpSessionKeysBrowser.preview, `"future"`) {
					t.Fatal("bounded original key detail lost unsigned or unknown values")
				}
				captureSessionHeldKeys(t, tt, language, size, "key-detail")
				workspaceClick(t, tt, "返回持有键列表")
				workspaceClick(t, tt, "筛选已返回的持有键")
				tt.Type("中文 000")
				tt.Frame()
				if len(d.httpSessionKeysBrowser.indices) != 1 || d.httpSessionKeysBrowser.page != 0 || d.httpSessionKeysBrowser.selectedKey != "" {
					t.Fatal("native filter retained stale selection or failed original-order projection")
				}
				captureSessionHeldKeys(t, tt, language, size, "key-filter")
				// Change only this captured ACL boundary for presentation; no new
				// server request or completeness authority is inferred by the view.
				d.last.HTTPSessionKeys.Complete = false
				d.last.Result.HTTP.Metadata.ACLFiltered = new(true)
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "ACL 已过滤；此持有键观察不完整")
				captureSessionHeldKeys(t, tt, language, size, "acl-filtered")
				workspaceClick(t, tt, "返回原 HTTP 回执")
				if d.Request() != original || l.reads.Load() != 1 || !bytes.Equal(body, d.last.Result.HTTP.Body) {
					t.Fatal("local held-key views rewrote original text/body or performed another root read")
				}
				s.Close(d)
				if d.httpSessionKeysBrowser != nil || d.httpSessionKeys {
					t.Fatal("close retained held-key modal or row state")
				}
			})
		}
	}
}
