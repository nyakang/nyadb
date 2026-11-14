package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpAgentTTLRequestTool(kind contract.KVRequestToolKind) bool {
	return kind == contract.KVToolHTTPAgentCheckPass || kind == contract.KVToolHTTPAgentCheckWarn || kind == contract.KVToolHTTPAgentCheckFail || kind == contract.KVToolHTTPAgentCheckUpdate
}

func (s *State) httpAgentTTLToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPAgentTTL
	ui.TextInput(c, &a.ID).Label(locales.Text("原生检查 ID")).FillWidth().Disabled(disabled)
	if kind == contract.KVToolHTTPAgentCheckUpdate {
		ui.Text(c, locales.Text("完整 TTL 状态 JSON（保留原始字节）"))
		ui.TextArea(c, &a.DefinitionJSON).Label(locales.Text("编辑完整 TTL 状态 JSON")).Height(125).FillWidth().ReadOnly(disabled)
		ui.Text(c, locales.Text("Status 为 passing、warning 或 critical；Output 的省略、空值和 null 保留原样")).SingleLine().Tooltip(locales.Text("Status 为 passing、warning 或 critical；Output 的省略、空值和 null 保留原样")).TextColor(theme.Colors(c).Muted)
	} else {
		ui.Checkbox(c, &a.IncludeNote, locales.Text("显式包含 note（空文本表示清空 Output）")).Disabled(disabled)
		ui.TextArea(c, &a.NoteJSON).Label(locales.Text("原始 note JSON 字符串")).Height(85).FillWidth().ReadOnly(disabled || !a.IncludeNote)
		ui.Text(c, locales.Text("使用 JSON 字符串保留换行和控制字符；省略或空文本均清空 Output，保留 Notes")).SingleLine().Tooltip(locales.Text("使用 JSON 字符串保留换行和控制字符；省略或空文本均清空 Output，保留 Notes")).TextColor(theme.Colors(c).Muted)
	}
	hint := locales.Text("更新原 ID 的本机 TTL 检查并重置计时；运行后另行确认")
	ui.Text(c, hint).SingleLine().Tooltip(hint).TextColor(theme.Colors(c).Muted)
}
