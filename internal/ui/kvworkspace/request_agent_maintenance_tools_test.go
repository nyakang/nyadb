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

func TestAgentMaintenanceFormsKeepExactInputsAndRequireOriginalWriteConfirmation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentNodeMaintenance, contract.KVToolHTTPAgentServiceMaintenance} {
				t.Run(fmt.Sprintf("%s-%dx%d-%s", language, size[0], size[1], kind), func(t *testing.T) {
					s, d, _, lease := requestToolTestState(t)
					d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind, Default: contract.KVRequestToolInput{HTTPAgentMaintenance: contract.KVHTTPAgentMaintenanceToolInput{Enable: "true"}}})
					s.OpenRequestTool(d, kind)
					tt := workspaceTester(s, d, size[0], size[1])
					tt.Frame()
					if kind == contract.KVToolHTTPAgentServiceMaintenance {
						workspaceClick(t, tt, "输入原生服务 ID")
						tt.Type("original 雪%?#++")
						tt.Frame()
					}
					workspaceClick(t, tt, "原生 enable 布尔值")
					tt.Key(ui.Ctrl, ui.KeyA)
					tt.Type("FALSE")
					tt.Frame()
					workspaceClick(t, tt, "显式包含 reason（省略或空值使用原生默认原因）")
					workspaceClick(t, tt, "原始 reason JSON 字符串")
					authored := `"  first\nsecond\r\nNUL\u0000雪 %?#++  "`
					tt.Type(authored)
					tt.Frame()
					input, ok := d.RequestToolInput(kind)
					if !ok || input.HTTPAgentMaintenance.Enable != "FALSE" || !input.HTTPAgentMaintenance.IncludeReason || input.HTTPAgentMaintenance.ReasonJSON != authored || kind == contract.KVToolHTTPAgentNodeMaintenance && input.HTTPAgentMaintenance.ID != "" || kind == contract.KVToolHTTPAgentServiceMaintenance && input.HTTPAgentMaintenance.ID != "original 雪%?#++" {
						t.Fatal("maintenance form changed original target, lexical enable or reason JSON")
					}
					assertHTTPSessionRenewalControls(t, tt, size, "原始 reason JSON 字符串", "生成请求文本", "复制工具输入", "返回请求文本")
					captureAgentUnit(t, tt, language, size, string(kind)+"-form")
					workspaceClick(t, tt, "复制工具输入")
					consume(t, s, d)
					var copy struct{ Input contract.KVRequestToolInput }
					if json.Unmarshal([]byte(tt.Clipboard()), &copy) != nil || copy.Input != input {
						t.Fatal("maintenance clipboard lost original comparable inputs")
					}
					raw, err := encodeRequestToolInput(kind, input)
					var decoded struct {
						Version int                         `json:"version"`
						Kind    contract.KVRequestToolKind  `json:"tool"`
						Input   contract.KVRequestToolInput `json:"input"`
					}
					if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Version != 1 || decoded.Kind != kind || decoded.Input != input {
						t.Fatal("maintenance request file lost original inputs", err)
					}
					workspaceClick(t, tt, "生成请求文本")
					if d.requestTools.open || d.Busy() || lease.executes.Load() != 0 || lease.connects.Load() != 0 {
						t.Fatal("maintenance form generation acquired execution authority")
					}
					s.Run(d)
					if d.confirmation == nil || lease.executes.Load() != 0 {
						t.Fatal("maintenance bypassed immutable original write confirmation")
					}
					s.Confirm(d, true)
					s.Confirm(d, true)
					consume(t, s, d)
					if lease.executes.Load() != 1 {
						t.Fatal("maintenance original confirmation dispatched more than once")
					}
					s.OpenRequestTool(d, kind)
					retained, _ := d.RequestToolInput(kind)
					if retained != input {
						t.Fatal("reopening maintenance form lost original inputs")
					}
				})
			}
		}
	}
}

func TestAgentMaintenanceFilesRejectInvalidUTF8AndCompleteInputOverflow(t *testing.T) {
	for _, field := range []string{"ID", "Enable", "ReasonJSON"} {
		input := contract.KVRequestToolInput{}
		value := string([]byte{255})
		switch field {
		case "ID":
			input.HTTPAgentMaintenance.ID = value
		case "Enable":
			input.HTTPAgentMaintenance.Enable = value
		case "ReasonJSON":
			input.HTTPAgentMaintenance.ReasonJSON = value
		}
		if _, err := encodeRequestToolInput(contract.KVToolHTTPAgentServiceMaintenance, input); err == nil {
			t.Fatal("maintenance file replaced invalid original UTF8", field)
		}
	}
	s, d, _, _ := requestToolTestState(t)
	kind := contract.KVToolHTTPAgentNodeMaintenance
	d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	s.OpenRequestTool(d, kind)
	s.SetRequestToolInput(d, kind, contract.KVRequestToolInput{HTTPAgentMaintenance: contract.KVHTTPAgentMaintenanceToolInput{ReasonJSON: strings.Repeat("x", inputLimit+1)}})
	retained, _ := d.RequestToolInput(kind)
	if retained != (contract.KVRequestToolInput{}) || d.error != locales.Text("KV 请求文本超过 16 MiB") {
		t.Fatal("maintenance draft exceeded its complete input budget")
	}
}
