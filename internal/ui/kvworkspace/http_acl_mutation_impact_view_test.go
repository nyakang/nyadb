package kvworkspace

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/locales"
)

func TestHTTPACLMutationImpactBilingualRecordsAndConfirmReturn(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l, _ := aclMutationWorkspace(t)
				s.OpenHTTPACLMutationForm(d)
				tt := workspaceTester(s, d, size[0], size[1])
				s.PrepareHTTPACLMutationForm(d)
				consume(t, s, d)
				confirmation := d.httpACLMutation.confirmation
				if confirmation == nil {
					t.Fatal("original impact confirmation missing")
				}
				tt.Frame()
				workspaceClick(t, tt, "查看 ACL 影响记录")
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "显示 ACL 影响记录（含秘密）", "复制完整 ACL 影响记录…", "返回 ACL 写入确认")
				captureACLUnit(t, tt, language, size, "write-impact-hidden")
				if d.httpACLMutation.form.impactPreview != "" {
					t.Fatal("impact revealed raw secrets without request")
				}
				workspaceClick(t, tt, "显示 ACL 影响记录（含秘密）")
				if !strings.Contains(d.httpACLMutation.form.impactPreview, "synthetic-sensitive-value") {
					t.Fatal("explicit impact reveal lost original body")
				}
				tt.Frame()
				workspaceClick(t, tt, "隐藏 ACL 写入秘密")
				workspaceClick(t, tt, "复制完整 ACL 影响记录…")
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "我确认传出此原输入或回执中的秘密", "确认传出 ACL 写入内容", "取消")
				captureACLUnit(t, tt, language, size, "write-impact-export")
				workspaceClick(t, tt, "取消")
				workspaceClick(t, tt, "返回 ACL 写入确认")
				tt.Frame()
				if d.httpACLMutation.confirmation != confirmation || d.httpACLMutation.form.checked || l.mutations.Load() != 0 {
					t.Fatal("viewing evidence changed original confirmation or executed")
				}
				assertHTTPSessionRenewalControls(t, tt, size, "我确认执行此原连接 ACL 写入", "确认执行 ACL 写入", "取消")
				workspaceClick(t, tt, "取消")
				if !confirmation.plan.(*aclMutationStatePlan).closed.Load() {
					t.Fatal("cancelled form retained original plan")
				}
				if locales.Text("ACL 影响预览与复核记录") == "" {
					t.Fatal("impact translation missing")
				}
			})
		}
	}
}
