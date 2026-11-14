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

func captureCatalogUnit(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_CATALOG_UNIT_IMAGES")
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

func TestHTTPCatalogBrowserBilingualOriginalPagesDetailsClipboardAndClose(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []string{"datacenters", "nodes", "services", "service", "node"} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, p := httpTestState(t)
					d.scope.HTTP.Protocol = "consul-http"
					d.last = catalogBrowserReceipt(53, kind)
					d.last.Scope = d.scope.Clone()
					d.last.Result.HTTP.Catalog.Complete = false
					d.last.Result.HTTP.Metadata.ACLFiltered = new(true)
					request := d.Request()
					body := bytes.Clone(d.last.Result.HTTP.Body)
					tt := workspaceTester(s, d, size[0], size[1])
					workspaceClick(t, tt, "Catalog 原响应浏览器")
					assertHTTPSessionRenewalControls(t, tt, size, "筛选已返回的 Catalog 条目", "下一页", "查看所选原始详情", "复制所选 Catalog 原值 JSON", "导出所选 Catalog 原值 JSON")
					if _, ok := tt.Find(locales.Text("Catalog 响应受 ACL 过滤或不完整；仅展示原响应返回的条目")); !ok {
						t.Fatal("Catalog original ACL boundary not visible")
					}
					captureCatalogUnit(t, tt, language, size, kind+"-list")
					workspaceClick(t, tt, "下一页")
					workspaceClick(t, tt, "original-catalog-052")
					index, ok := d.httpCatalogBrowser.selectedIndex(d.last)
					if !ok {
						t.Fatal("Catalog row selection unavailable")
					}
					want, ok := catalogRowRaw(d.last, index)
					if !ok {
						t.Fatal("selected original Catalog raw value unavailable")
					}
					workspaceClick(t, tt, "复制所选 Catalog 原值 JSON")
					consume(t, s, d)
					if tt.Clipboard() != string(want) {
						t.Fatal("Catalog clipboard changed complete original selected value", "selected", d.httpCatalogBrowser.selectedOriginal, "bytes", len(tt.Clipboard()), "error", d.Error())
					}
					workspaceClick(t, tt, "查看所选原始详情")
					if !d.httpCatalogBrowser.detail || d.httpCatalogBrowser.preview == "" {
						t.Fatal("Catalog original detail unavailable")
					}
					if kind == "nodes" || kind == "service" || kind == "node" {
						if !strings.Contains(d.httpCatalogBrowser.preview, "9007199254740993") {
							t.Fatal("Catalog detail lost exact original unknown number")
						}
					}
					captureCatalogUnit(t, tt, language, size, kind+"-detail")
					workspaceClick(t, tt, "返回 Catalog 列表")
					if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) || p.lease.executes.Load() != 0 || p.lease.connects.Load() != 0 {
						t.Fatal("local Catalog browser dispatched or changed original receipt")
					}
					s.Close(d)
					if d.httpCatalogBrowser != nil || d.httpCatalog {
						t.Fatal("close retained Catalog browser cache")
					}
				})
			}
		}
	}
}

func TestCatalogToolFormsBilingualDraftCopyAndGenerationNeverDispatches(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	kinds := []contract.KVRequestToolKind{contract.KVToolHTTPCatalogDatacenters, contract.KVToolHTTPCatalogNodes, contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogService, contract.KVToolHTTPCatalogNode}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range kinds {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, _, l := requestToolTestState(t)
					for _, tool := range kinds {
						d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: tool})
					}
					s.OpenRequestTool(d, kind)
					input := contract.KVRequestToolInput{}
					if kind != contract.KVToolHTTPCatalogDatacenters {
						input.HTTPCatalog = contract.KVHTTPCatalogToolInput{IncludeFilter: true, Filter: "partial ` 原值", Index: "18446744073709551615", Wait: "1ms"}
						if kind == contract.KVToolHTTPCatalogNodes || kind == contract.KVToolHTTPCatalogService {
							input.HTTPCatalog.IncludeNear = true
						}
					}
					if kind == contract.KVToolHTTPCatalogNode {
						input.HTTPCatalog.Node = "  原节点 %?#++  "
					}
					if kind == contract.KVToolHTTPCatalogService {
						input.HTTPCatalog.Service = "  原服务 %?#++  "
					}
					s.SetRequestToolInput(d, kind, input)
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					assertHTTPSessionRenewalControls(t, tt, size, "请求工具操作", "生成请求文本", "复制工具输入", "返回请求文本")
					captureCatalogUnit(t, tt, language, size, string(kind)+"-form")
					workspaceClick(t, tt, "复制工具输入")
					consume(t, s, d)
					want, err := encodeRequestToolInput(kind, input)
					if err != nil || tt.Clipboard() != string(want) {
						t.Fatal("Catalog form copy lost complete authored fields", err)
					}
					original := d.Request()
					workspaceClick(t, tt, "生成请求文本")
					if d.Request() == original || d.requestTools.open || d.Busy() || l.executes.Load() != 0 || l.connects.Load() != 0 {
						t.Fatal("Catalog form generation dispatched or retained modal")
					}
				})
			}
		}
	}
}
