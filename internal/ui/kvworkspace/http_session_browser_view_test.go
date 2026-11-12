package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func captureSessionBrowser(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_SESSION_BROWSER_IMAGES")
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

func TestHTTPSessionBrowserBilingualCapturedPagesDetailsAndClose(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, provider := httpTestState(t)
				d.scope.HTTP.Protocol = "consul-http"
				rows := make([]contract.KVHTTPSession, 53)
				native := make([]map[string]any, len(rows))
				for i := range rows {
					rows[i] = contract.KVHTTPSession{ID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", i), Name: fmt.Sprintf("Session 中文 %d", i), Node: "node", TTL: "10m", Behavior: "release", ModifyIndex: math.MaxUint64, PresentFields: []string{"ID", "Name", "Node", "TTL", "Behavior", "ModifyIndex"}}
					native[i] = map[string]any{"ID": rows[i].ID, "Name": rows[i].Name, "Node": "node", "TTL": "10m", "Behavior": "release", "ModifyIndex": uint64(math.MaxUint64), "future": map[string]any{"null": nil}}
				}
				d.last = sessionBrowserReceipt(rows)
				d.last.Scope = d.scope.Clone()
				d.last.Result.HTTP.Body, _ = json.Marshal(native)
				originalBody := bytes.Clone(d.last.Result.HTTP.Body)
				request := d.Request()
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "Session 列表")
				captureSessionBrowser(t, tt, language, size, "list")
				assertHTTPSessionRenewalControls(t, tt, size, "筛选已返回的 Session", "Session TTL 筛选", "下一页", "查看所选原始详情")
				table, found := tt.Find(locales.Text("已返回的 Session 列表"))
				if !found {
					t.Fatal("captured Session table missing")
				}
				tt.Scroll(table.X+20, table.Y+table.H-25, 0, 1000)
				tt.Frame()
				workspaceClick(t, tt, "下一页")
				workspaceClick(t, tt, rows[52].ID)
				workspaceClick(t, tt, "复制所选 Session UUID")
				if tt.Clipboard() != rows[52].ID {
					t.Fatal("copied UUID differs from original selected last-page row")
				}
				workspaceClick(t, tt, "查看所选原始详情")
				if !strings.Contains(d.httpSessionBrowser.preview, "18446744073709551615") || !strings.Contains(d.httpSessionBrowser.preview, `"future"`) {
					t.Fatal("original detail lost unsigned precision or unknown field values")
				}
				captureSessionBrowser(t, tt, language, size, "detail")
				workspaceClick(t, tt, "返回 Session 列表")
				if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, originalBody) || provider.lease.connects.Load() != 0 || provider.lease.executes.Load() != 0 {
					t.Fatal("local Session interaction changed original evidence or performed I/O")
				}
				s.Close(d)
				if d.httpSessionBrowser != nil || d.httpSessions {
					t.Fatal("document close retained Session browser state")
				}
			})
		}
	}
}

func TestHTTPSessionBrowserCellsKeepNullMissingEmptyAndExactUnsigned(t *testing.T) {
	row := contract.KVHTTPSession{PresentFields: []string{"Name", "Node", "ModifyIndex"}, NullFields: []string{"Node"}, ModifyIndex: math.MaxUint64}
	if sessionCellPreview(row, 1) != `""` || sessionCellPreview(row, 2) != "null" || sessionCellPreview(row, 3) != locales.Text("（未返回）") || sessionCellPreview(row, 6) != "18446744073709551615" {
		t.Fatal("Session cells conflated original field states or rounded indices")
	}
	row.Name = strings.Repeat("中", 1000)
	preview := sessionCellPreview(row, 1)
	if len(preview) > 259 || !utf8.ValidString(preview) || row.Name != strings.Repeat("中", 1000) {
		t.Fatal("bounded cell preview changed original text or split Unicode")
	}
}

func TestHTTPSessionBrowserOriginalDetailDoesNotTreatUnknownFieldsAsUUIDAliases(t *testing.T) {
	r := sessionBrowserReceipt([]contract.KVHTTPSession{{ID: "original"}})
	r.Result.HTTP.Body = []byte(`[{"ID":"original","Id":"foreign","unknown":18446744073709551615}]`)
	preview := originalSessionPreview(r, "original")
	if !strings.Contains(preview, `"ID": "original"`) || !strings.Contains(preview, `"Id": "foreign"`) || !strings.Contains(preview, "18446744073709551615") || originalSessionPreview(r, "foreign") != "" {
		t.Fatal("detail treated a differently cased unknown field as original identity")
	}
}

func TestHTTPSessionBrowserPrivateReadFormsPreserveOriginalNodeWithoutDispatch(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions} {
				s, d, _, lease := requestToolTestState(t)
				d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
				s.OpenRequestTool(d, kind)
				input := contract.KVRequestToolInput{}
				if kind == contract.KVToolHTTPNodeSessions {
					input.HTTPSession.Node = " 原始 node ?%# "
				}
				s.SetRequestToolInput(d, kind, input)
				original := d.Request()
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "请求工具操作", "复制工具输入", "生成请求文本")
				if kind == contract.KVToolHTTPNodeSessions {
					assertHTTPSessionRenewalControls(t, tt, size, "原节点名称")
				}
				captureSessionBrowser(t, tt, language, size, string(kind)+"-form")
				workspaceClick(t, tt, "生成请求文本")
				var generated struct {
					Method string
					Input  contract.KVRequestToolInput
				}
				if json.Unmarshal([]byte(d.Request().JSON), &generated) != nil || generated.Method != "GET" || generated.Input != input || original == d.Request() || d.confirmation != nil || lease.executes.Load() != 0 || lease.connects.Load() != 0 {
					t.Fatal("read form changed original node or dispatched")
				}
			}
		}
	}
}
