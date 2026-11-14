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
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func captureACLUnit(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_ACL_UNIT_IMAGES")
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
		t.Fatal("could not capture synthetic ACL view")
	}
}

func TestHTTPACLBrowserBilingualRevealConsentAndOriginalPages(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l := aclTransferFixture(t)
				r := d.httpACL.last
				r.Projection.Rows = nil
				for i := range 53 {
					id := fmt.Sprintf("original-acl-%03d", i)
					raw := []byte(fmt.Sprintf(`{"AccessorID":%q,"SecretID":"synthetic-secret-%d","ModifyIndex":18446744073709551615}`, id, i))
					r.Projection.Rows = append(r.Projection.Rows, contract.KVHTTPACLRow{Identity: id, Raw: raw, SecretState: "value", ConfigState: "absent"})
				}
				d.httpACL.browser = nil
				original := r.Clone()
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "ACL 原响应浏览器")
				assertHTTPSessionRenewalControls(t, tt, size, "筛选已返回的 ACL 名称或标识", "下一页", "显示所选 ACL 原值（含秘密）", "复制所选 ACL 原值…")
				if d.httpACL.revealed != "" || d.httpACL.browser.preview != "" || len(aclBrowserIndices(r.Projection, "synthetic-secret")) != 0 {
					t.Fatal("default list or search revealed private fields")
				}
				captureACLUnit(t, tt, language, size, "hidden-list")
				workspaceClick(t, tt, "下一页")
				workspaceClick(t, tt, "original-acl-052")
				if d.httpACL.browser.selected != 52 {
					t.Fatal("ACL paging changed original row identity")
				}
				workspaceClick(t, tt, "显示所选 ACL 原值（含秘密）")
				if d.httpACL.revealed != r.LocalRecordID || !strings.Contains(d.httpACL.browser.preview, "synthetic-secret-52") || !strings.Contains(d.httpACL.browser.preview, "18446744073709551615") {
					t.Fatal("explicit detail lost original secret or native precision")
				}
				captureACLUnit(t, tt, language, size, "revealed-synthetic-detail")
				workspaceClick(t, tt, "隐藏秘密并返回 ACL 列表")
				if d.httpACL.revealed != "" || d.httpACL.browser.preview != "" {
					t.Fatal("hidden ACL preview retained secrets")
				}
				workspaceClick(t, tt, "复制所选 ACL 原值…")
				if d.httpACL.transfer == nil || d.FileBusy() || tt.Clipboard() != "" {
					t.Fatal("ACL copy skipped explicit secret confirmation")
				}
				assertHTTPSessionRenewalControls(t, tt, size, "我确认复制或导出此原回执中的秘密", "确认传出 ACL 内容", "取消")
				captureACLUnit(t, tt, language, size, "copy-consent")
				workspaceClick(t, tt, "我确认复制或导出此原回执中的秘密")
				workspaceClick(t, tt, "确认传出 ACL 内容")
				consume(t, s, d)
				if tt.Clipboard() != string(original.Projection.Rows[52].Raw) || l.reads.Load() != 1 || l.executes.Load() != 0 || !bytes.Equal(r.Receipt.HTTP.Body, original.Receipt.HTTP.Body) {
					t.Fatal("explicit clipboard changed original bytes or executed a request")
				}
				workspaceClick(t, tt, "返回原 HTTP 回执")
				if d.httpACL.browser.open || d.httpACL.transfer != nil || d.httpACL.revealed != "" {
					t.Fatal("closed private browser retained secret authority")
				}
				s.Close(d)
				if d.httpACL != nil {
					t.Fatal("document close retained private view state")
				}
			})
		}
	}
}

func TestHTTPACLBrowserUnavailableAndRedactedReceiptsRemainDistinct(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })
	for _, kind := range []string{"denied", "absent", "null", "redacted", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _ := aclTransferFixture(t)
			r := d.httpACL.last
			switch kind {
			case "denied":
				r.Receipt.HTTP.Status, r.Projection = 403, nil
			case "absent":
				r.Receipt.HTTP.Status, r.Projection = 404, nil
			case "null":
				r.Projection.Null, r.Projection.Complete, r.Projection.Rows = true, false, nil
			case "redacted":
				r.Projection.ContainsRedaction = true
				r.Projection.Rows[0].SecretState = "redacted"
			case "malformed":
				r.Projection, r.ProjectionError = nil, "ACL projection unavailable; original receipt retained"
			}
			tt := workspaceTester(s, d, 720, 520)
			tt.Frame()
			label := "ACL 条目不可用；原始回执仍可显式查看或导出"
			if kind == "null" || kind == "redacted" {
				label = "仅展示原响应返回的 ACL 条目；过滤、null 或遮蔽不表示资源不存在"
			}
			if _, ok := tt.Find(locales.Text(label)); !ok || d.httpACL.revealed != "" {
				t.Fatal("native ACL absence, denial or redaction boundary missing")
			}
			captureACLUnit(t, tt, "en", [2]int{720, 520}, kind)
		})
	}
}
