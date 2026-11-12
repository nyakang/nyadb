package kvworkspace

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func captureSessionTool(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	folder := os.Getenv("NYADB_CONSUL_SESSION_TOOLS_IMAGES")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-workspace-session-tool-%s.png", language, size[0], size[1], stage)))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestSessionToolFormsBilingualOriginalInputsAndSeparateConfirmation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, l := sessionRequestToolsTestState(t)
					s.OpenRequestTool(d, kind)
					input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: renewalUUID}}
					if kind == contract.KVToolHTTPSessionCreate {
						input.HTTPSession = contract.KVHTTPSessionToolInput{IncludeName: true, Name: "", TTL: "10s", LockDelay: "0s", Behavior: "release", NodeChecksJSON: "[]"}
					} else if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
						input.Key, input.Flags, input.ValueBase64 = "original/中文", "18446744073709551615", "AP8="
					}
					s.SetRequestToolInput(d, kind, input)
					original := d.Request()
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					labels := []string{"请求工具操作", "复制工具输入", "导出工具输入", "返回请求文本", "生成请求文本"}
					if kind == contract.KVToolHTTPSessionCreate {
						labels = append(labels, "包含 Session 名称", "包含 Session 节点", "TTL", "LockDelay", "Behavior", "NodeChecks JSON（留空省略，[] 为无检查）")
					} else {
						labels = append(labels, "Session UUID")
					}
					if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
						labels = append(labels, "绝对键", "Flags (uint64)", "原值 Base64（空文本表示空字节值）")
					}
					assertHTTPSessionRenewalControls(t, tt, size, labels...)
					if language == "en" && (strings.ContainsFunc(requestToolName(kind), func(r rune) bool { return unicode.Is(unicode.Han, r) }) || requestToolName(kind) == string(kind)) {
						t.Fatal("new tool name lacks translation")
					}
					column, _ := tt.Find(locales.Text("请求工具操作"))
					for _, action := range []string{"复制工具输入", "导出工具输入", "返回请求文本", "生成请求文本"} {
						r, _ := tt.Find(locales.Text(action))
						if r.X < column.X || r.X+r.W > column.X+column.W+1 {
							t.Fatal("tool action outside modal column", action, r, column)
						}
					}
					captureSessionTool(t, tt, language, size, string(kind)+"-form")
					if d.Request() != original || l.connects.Load() != 0 || l.executes.Load() != 0 {
						t.Fatal("rendering form changed request or performed I/O")
					}
					workspaceClick(t, tt, "生成请求文本")
					if d.requestTools.open || d.Busy() || d.confirmation != nil || l.executes.Load() != 0 {
						t.Fatal("form generation dispatched")
					}
					if kind != contract.KVToolHTTPSessionInfo {
						s.Run(d)
						if d.confirmation == nil || l.executes.Load() != 0 {
							t.Fatal("generated Session write skipped separate confirmation")
						}
					}
				})
			}
		}
	}
}

func TestSessionToolValidationChangesDisplayLanguageWithoutChangingOriginalText(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	s, d, _ := sessionRequestToolsTestState(t)
	s.OpenRequestTool(d, contract.KVToolHTTPAcquire)
	input := contract.KVRequestToolInput{Key: "invalid", HTTPSession: contract.KVHTTPSessionToolInput{ID: renewalUUID}}
	s.SetRequestToolInput(d, contract.KVToolHTTPAcquire, input)
	original := d.Request()
	s.GenerateRequestTool(d)
	originalError := d.Error()
	if originalError != locales.Text("KV 请求无效")+": invalid tool input" {
		t.Fatal("missing original validation error")
	}
	tt := workspaceTester(s, d, 720, 520)
	i18n.SetLocaleFunc(func() string { return "en" })
	tt.Frame()
	if _, ok := tt.Find(originalError); ok {
		t.Fatal("stale source-language validation error displayed")
	}
	if _, ok := tt.Find(locales.Text("KV 请求无效") + ": invalid tool input"); !ok {
		t.Fatal("current-language validation error missing")
	}
	got, _ := d.RequestToolInput(contract.KVToolHTTPAcquire)
	if d.Request() != original || got != input || d.Error() != originalError {
		t.Fatal("display translation rewrote original input or diagnostic")
	}
}

func TestSessionToolPresenceTogglesClearOmittedValues(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			s, d, l := sessionRequestToolsTestState(t)
			s.OpenRequestTool(d, contract.KVToolHTTPSessionCreate)
			input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: "original name", IncludeNode: true, Node: "original node", TTL: "10m"}}
			s.SetRequestToolInput(d, contract.KVToolHTTPSessionCreate, input)
			original := d.Request()
			tt := workspaceTester(s, d, 720, 520)
			tt.Frame()
			for _, label := range []string{"包含 Session 名称", "包含 Session 节点"} {
				workspaceClick(t, tt, label)
			}
			got, _ := d.RequestToolInput(contract.KVToolHTTPSessionCreate)
			if got.HTTPSession.IncludeName || got.HTTPSession.Name != "" || got.HTTPSession.IncludeNode || got.HTTPSession.Node != "" {
				t.Fatal("omitted Session field retained hidden text")
			}
			for _, label := range []string{"包含 Session 名称", "包含 Session 节点"} {
				workspaceClick(t, tt, label)
			}
			got, _ = d.RequestToolInput(contract.KVToolHTTPSessionCreate)
			if !got.HTTPSession.IncludeName || !got.HTTPSession.IncludeNode || got.HTTPSession.Name != "" || got.HTTPSession.Node != "" || d.Request() != original || l.executes.Load() != 0 {
				t.Fatal("reenabling Session fields did not preserve explicit empty draft")
			}
		})
	}
}
