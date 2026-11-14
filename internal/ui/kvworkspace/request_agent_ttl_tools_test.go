package kvworkspace

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestAgentTTLFormsBilingualOriginalInputCopyGenerationAndConfirmation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckWarn, contract.KVToolHTTPAgentCheckFail, contract.KVToolHTTPAgentCheckUpdate} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, _, lease := requestToolTestState(t)
					d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
					s.OpenRequestTool(d, kind)
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					label, authored := "原生检查 ID", "original 雪%?#++"
					if kind == contract.KVToolHTTPAgentCheckUpdate {
						workspaceClick(t, tt, "原生检查 ID")
						tt.Type(authored)
						tt.Frame()
						label = "编辑完整 TTL 状态 JSON"
						authored = " {\n\"Status\":\"warning\",\"Output\":null,\"UnknownNative\":18446744073709551615\n} "
					}
					workspaceClick(t, tt, label)
					tt.Key(ui.Ctrl, ui.KeyA)
					tt.Type(authored)
					tt.Frame()
					input, ok := d.RequestToolInput(kind)
					if !ok || input.HTTPAgentTTL.ID != "original 雪%?#++" || kind == contract.KVToolHTTPAgentCheckUpdate && input.HTTPAgentTTL.DefinitionJSON != authored || kind != contract.KVToolHTTPAgentCheckUpdate && input.HTTPAgentTTL.ID != authored {
						t.Fatalf("form changed authored native input: definition=%q ID=%q expected=%q", input.HTTPAgentTTL.DefinitionJSON, input.HTTPAgentTTL.ID, authored)
					}
					if kind != contract.KVToolHTTPAgentCheckUpdate {
						workspaceClick(t, tt, "显式包含 note（空文本表示清空 Output）")
						workspaceClick(t, tt, "原始 note JSON 字符串")
						tt.Type(`"original first\nsecond\r\nNUL\u0000雪"`)
						tt.Frame()
						input, _ = d.RequestToolInput(kind)
						if !input.HTTPAgentTTL.IncludeNote || input.HTTPAgentTTL.NoteJSON != `"original first\nsecond\r\nNUL\u0000雪"` {
							t.Fatalf("TTL note form changed original: include=%v note=%q", input.HTTPAgentTTL.IncludeNote, input.HTTPAgentTTL.NoteJSON)
						}
					}
					assertHTTPSessionRenewalControls(t, tt, size, label, "生成请求文本", "复制工具输入", "返回请求文本")
					captureAgentUnit(t, tt, language, size, string(kind)+"-form")
					workspaceClick(t, tt, "复制工具输入")
					consume(t, s, d)
					var copy struct{ Input contract.KVRequestToolInput }
					if json.Unmarshal([]byte(tt.Clipboard()), &copy) != nil || copy.Input != input {
						t.Fatal("tool input clipboard lost original definition or ID")
					}
					workspaceClick(t, tt, "生成请求文本")
					if d.requestTools.open || d.Busy() || lease.executes.Load() != 0 || lease.connects.Load() != 0 {
						t.Fatal("form generation acquired execution authority")
					}
					s.Run(d)
					if d.confirmation == nil || lease.executes.Load() != 0 {
						t.Fatal("check write bypassed original confirmation")
					}
					s.Confirm(d, true)
					s.Confirm(d, true)
					consume(t, s, d)
					if lease.executes.Load() != 1 {
						t.Fatal("check confirmation dispatched more than once")
					}
					s.OpenRequestTool(d, kind)
					retained, _ := d.RequestToolInput(kind)
					if retained != input {
						t.Fatal("reopening form lost original input")
					}
				})
			}
		}
	}
}

func TestAgentTTLDraftFilesRejectInvalidUTF8AndCountCompleteInputs(t *testing.T) {
	for _, field := range []string{"ID", "NoteJSON", "DefinitionJSON"} {
		input := contract.KVRequestToolInput{}
		value := string([]byte{255})
		switch field {
		case "ID":
			input.HTTPAgentTTL.ID = value
		case "NoteJSON":
			input.HTTPAgentTTL.NoteJSON = value
		case "DefinitionJSON":
			input.HTTPAgentTTL.DefinitionJSON = value
		}
		if _, err := encodeRequestToolInput(contract.KVToolHTTPAgentCheckUpdate, input); err == nil {
			t.Fatal("file silently replaced invalid original UTF8", field)
		}
	}
	s, d, _, _ := requestToolTestState(t)
	kind := contract.KVToolHTTPAgentCheckUpdate
	d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPAgentTTL: contract.KVHTTPAgentTTLToolInput{DefinitionJSON: strings.Repeat("x", inputLimit+1)}}
	s.SetRequestToolInput(d, kind, input)
	retained, _ := d.RequestToolInput(kind)
	if retained != (contract.KVRequestToolInput{}) || d.error != locales.Text("KV 请求文本超过 16 MiB") {
		t.Fatal("oversized original definition escaped draft budget")
	}
}
