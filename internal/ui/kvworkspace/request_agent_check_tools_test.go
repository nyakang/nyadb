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

func TestAgentCheckFormsBilingualOriginalInputCopyGenerationAndConfirmation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckRegister, contract.KVToolHTTPAgentCheckDeregister} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, _, lease := requestToolTestState(t)
					d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
					s.OpenRequestTool(d, kind)
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					label, authored := "原生检查 ID", "original 雪%?#++"
					if kind == contract.KVToolHTTPAgentCheckRegister {
						label = "编辑完整检查定义 JSON"
						authored = " {\n\"Name\":\"original\",\"TTL\":\"10m\",\"ID\":null,\"Notes\":\"原值 18446744073709551615\"\n} "
					}
					workspaceClick(t, tt, label)
					tt.Key(ui.Ctrl, ui.KeyA)
					tt.Type(authored)
					tt.Frame()
					input, ok := d.RequestToolInput(kind)
					if !ok || kind == contract.KVToolHTTPAgentCheckRegister && input.HTTPAgentCheck.DefinitionJSON != authored || kind == contract.KVToolHTTPAgentCheckDeregister && input.HTTPAgentCheck.ID != authored {
						t.Fatalf("form changed authored native input: definition=%q ID=%q expected=%q", input.HTTPAgentCheck.DefinitionJSON, input.HTTPAgentCheck.ID, authored)
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

func TestAgentCheckDraftFilesRejectInvalidUTF8AndCountCompleteDefinition(t *testing.T) {
	for _, field := range []string{"ID", "DefinitionJSON"} {
		input := contract.KVRequestToolInput{}
		value := string([]byte{255})
		switch field {
		case "ID":
			input.HTTPAgentCheck.ID = value
		case "DefinitionJSON":
			input.HTTPAgentCheck.DefinitionJSON = value
		}
		if _, err := encodeRequestToolInput(contract.KVToolHTTPAgentCheckRegister, input); err == nil {
			t.Fatal("file silently replaced invalid original UTF8", field)
		}
	}
	s, d, _, _ := requestToolTestState(t)
	kind := contract.KVToolHTTPAgentCheckRegister
	d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: strings.Repeat("x", inputLimit+1)}}
	s.SetRequestToolInput(d, kind, input)
	retained, _ := d.RequestToolInput(kind)
	if retained != (contract.KVRequestToolInput{}) || d.error != locales.Text("KV 请求文本超过 16 MiB") {
		t.Fatal("oversized original definition escaped draft budget")
	}
}
