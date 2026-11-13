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

func captureAgentUnit(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_AGENT_UNIT_IMAGES")
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

func TestHTTPAgentBrowserBilingualOriginalMapsPagesDetailsClipboardAndClose(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []string{"services", "checks"} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, p := httpTestState(t)
					d.scope.HTTP.Protocol = "consul-http"
					d.last = agentBrowserReceipt(53, kind)
					d.last.Scope = d.scope.Clone()
					d.last.Result.HTTP.Agent.Complete = false
					d.last.Result.HTTP.Metadata.ACLFiltered = new(true)
					request := d.Request()
					body := bytes.Clone(d.last.Result.HTTP.Body)
					tt := workspaceTester(s, d, size[0], size[1])
					workspaceClick(t, tt, "本机 Agent 服务与检查")
					assertHTTPSessionRenewalControls(t, tt, size, "筛选已返回的 Agent 条目", "下一页", "查看所选原始详情", "复制所选原 map 值 JSON", "导出所选原 map 值 JSON")
					if _, ok := tt.Find(locales.Text("Agent 响应受 ACL 过滤或不完整；仅展示原 map 返回的条目")); !ok {
						t.Fatal("original Agent ACL boundary not visible")
					}
					captureAgentUnit(t, tt, language, size, kind+"-list")
					workspaceClick(t, tt, "下一页")
					workspaceClick(t, tt, "original-agent-052")
					workspaceClick(t, tt, "复制所选原 map 值 JSON")
					consume(t, s, d)
					if tt.Clipboard() != string(d.last.Result.HTTP.Agent.Rows[52].Raw) {
						t.Fatal("Agent clipboard changed original map value", "selected", d.httpAgentBrowser.selectedOriginal, "bytes", len(tt.Clipboard()), "file busy", d.FileBusy(), "error", d.Error())
					}
					workspaceClick(t, tt, "查看所选原始详情")
					if !strings.Contains(d.httpAgentBrowser.preview, "18446744073709551615") || !strings.Contains(d.httpAgentBrowser.preview, "original-agent-052") {
						t.Fatal("Agent detail lost original exact numbers/map identity")
					}
					captureAgentUnit(t, tt, language, size, kind+"-detail")
					workspaceClick(t, tt, "返回 Agent 列表")
					if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) || p.lease.executes.Load() != 0 || p.lease.connects.Load() != 0 {
						t.Fatal("local Agent browser read or modified original receipt")
					}
					s.Close(d)
					if d.httpAgentBrowser != nil || d.httpAgent {
						t.Fatal("close retained Agent browser cache")
					}
				})
			}
		}
	}
}

func TestAgentToolFormsBilingualDraftCopyAndGenerationNeverDispatches(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, _, l := requestToolTestState(t)
					for _, tool := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
						d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: tool})
					}
					s.OpenRequestTool(d, kind)
					input := contract.KVRequestToolInput{}
					if kind != contract.KVToolHTTPAgentSelf {
						input.HTTPAgent = contract.KVHTTPAgentToolInput{IncludeFilter: true, Filter: "原始 ` filter"}
					}
					s.SetRequestToolInput(d, kind, input)
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					assertHTTPSessionRenewalControls(t, tt, size, "请求工具操作", "生成请求文本", "复制工具输入", "返回请求文本")
					captureAgentUnit(t, tt, language, size, string(kind)+"-form")
					workspaceClick(t, tt, "复制工具输入")
					consume(t, s, d)
					want, err := encodeRequestToolInput(kind, input)
					if err != nil || tt.Clipboard() != string(want) {
						t.Fatal("Agent form copy changed authored fields", err)
					}
					original := d.Request()
					workspaceClick(t, tt, "生成请求文本")
					if d.Request() == original || d.requestTools.open || d.Busy() || l.executes.Load() != 0 || l.connects.Load() != 0 {
						t.Fatal("Agent form generation dispatched or retained modal")
					}
				})
			}
		}
	}
}
