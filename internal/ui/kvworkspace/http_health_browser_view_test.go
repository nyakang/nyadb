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

func captureHealthUnit(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_HEALTH_UNIT_IMAGES")
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

func TestHTTPHealthBrowserBilingualOriginalPagesDetailsClipboardAndClose(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, p := httpTestState(t)
				d.scope.HTTP.Protocol = "consul-http"
				d.last = healthBrowserReceipt(53, false)
				d.last.Scope = d.scope.Clone()
				body := bytes.Clone(d.last.Result.HTTP.Body)
				request := d.Request()
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "健康检查与服务实例")
				captureHealthUnit(t, tt, language, size, "checks-list")
				assertHTTPSessionRenewalControls(t, tt, size, "筛选已返回的健康观察", "检查状态筛选", "下一页", "查看所选原始详情", "复制所选原始 JSON", "导出所选原始 JSON")
				workspaceClick(t, tt, "下一页")
				workspaceClick(t, tt, "原检查 052")
				workspaceClick(t, tt, "复制所选原始 JSON")
				consume(t, s, d)
				want, err := originalHealthRow(body, 52)
				if err != nil || tt.Clipboard() != string(want) {
					t.Fatal("health clipboard lost original selected position/JSON", err, "selected", d.httpHealthBrowser.selectedOriginal, "clipboard bytes", len(tt.Clipboard()), "want bytes", len(want), "file busy", d.FileBusy(), "error", d.Error())
				}
				workspaceClick(t, tt, "查看所选原始详情")
				if !strings.Contains(d.httpHealthBrowser.preview, "18446744073709551615") || !strings.Contains(d.httpHealthBrowser.preview, `"Namespace": "52"`) || !strings.Contains(d.httpHealthBrowser.preview, `"future"`) {
					t.Fatal("health detail changed exact indices, qualified identity or unknown original fields")
				}
				captureHealthUnit(t, tt, language, size, "checks-detail")
				workspaceClick(t, tt, "返回健康列表")
				if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) || p.lease.executes.Load() != 0 || p.lease.connects.Load() != 0 {
					t.Fatal("local health view executed or modified original receipt")
				}
				s.Close(d)
				if d.httpHealthBrowser != nil || d.httpHealth {
					t.Fatal("document close retained health browser state")
				}
			})
		}
	}
}

func TestHealthToolFormsBilingualReadOptionsAndGeneratedText(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, l := healthRequestToolsTestState(t)
					s.OpenRequestTool(d, kind)
					h := contract.KVHTTPHealthToolInput{IncludeFilter: true, Filter: "Status == `passing`", IncludeNear: true, Near: "_agent", Index: "18446744073709551615", Wait: "1ms"}
					switch kind {
					case contract.KVToolHTTPHealthState:
						h.State = "any"
					case contract.KVToolHTTPHealthNode:
						h.Node = "原节点"
					default:
						h.Service = "原服务"
					}
					if kind == contract.KVToolHTTPHealthService {
						h.Passing = "false"
					}
					s.SetRequestToolInput(d, kind, contract.KVRequestToolInput{HTTPHealth: h})
					original := d.Request()
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					assertHTTPSessionRenewalControls(t, tt, size, "请求工具操作", "生成请求文本", "复制工具输入", "返回请求文本", "包含原生 filter", "包含原生 near", "index (uint64)", "wait")
					captureHealthUnit(t, tt, language, size, string(kind)+"-form")
					workspaceClick(t, tt, "复制工具输入")
					consume(t, s, d)
					want, err := encodeRequestToolInput(kind, contract.KVRequestToolInput{HTTPHealth: h})
					if err != nil || tt.Clipboard() != string(want) || d.Request() != original {
						t.Fatal("form copy lost exact authored options", err)
					}
					workspaceClick(t, tt, "生成请求文本")
					if d.Request() == original || d.requestTools.open || d.Busy() || l.executes.Load() != 0 || l.connects.Load() != 0 {
						t.Fatal("health form generation dispatched")
					}
				})
			}
		}
	}
}

func TestHTTPHealthBrowserBilingualInstancesAndACLSubsetWarning(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, p := httpTestState(t)
			d.scope.HTTP.Protocol = "consul-http"
			d.last = healthBrowserReceipt(53, true)
			d.last.Scope = d.scope.Clone()
			d.last.Result.HTTP.Health.Complete = false
			d.last.Result.HTTP.Metadata.ACLFiltered = new(true)
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "健康检查与服务实例")
			if _, ok := tt.Find(locales.Text("健康响应不完整或受 ACL 过滤；仅展示原响应返回的观察")); !ok {
				t.Fatal("ACL boundary warning missing")
			}
			workspaceClick(t, tt, "下一页")
			if d.httpHealthBrowser.page != 1 || len(d.httpHealthBrowser.pageIndices()) != 3 {
				t.Fatal("original service instance page missing")
			}
			captureHealthUnit(t, tt, language, size, "instances-acl-last-page")
			if p.lease.executes.Load() != 0 || p.lease.connects.Load() != 0 {
				t.Fatal("local instance view executed")
			}
			s.Close(d)
		}
	}
}
