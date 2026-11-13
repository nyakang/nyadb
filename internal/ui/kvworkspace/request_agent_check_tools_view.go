package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpAgentCheckRequestTool(kind contract.KVRequestToolKind) bool {
	return kind == contract.KVToolHTTPAgentCheckRegister || kind == contract.KVToolHTTPAgentCheckDeregister
}

func (s *State) httpAgentCheckToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPAgentCheck
	if kind == contract.KVToolHTTPAgentCheckRegister {
		ui.Text(c, locales.Text("完整检查定义 JSON（保留原始字节）"))
		ui.TextArea(c, &a.DefinitionJSON).Label(locales.Text("编辑完整检查定义 JSON")).Height(150).FillWidth().ReadOnly(disabled)
		ui.Text(c, locales.Text("保留名称、ID 的省略或空值和完整定义；已有 TTL 检查可能保留原状态")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
	} else {
		ui.TextInput(c, &a.ID).Label(locales.Text("原生检查 ID")).FillWidth().Disabled(disabled)
		ui.Text(c, locales.Text("注销原 ID 的本机检查；关联服务保持原样，运行后另行确认")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
	}
	hint := locales.Text("操作当前连接的本机 Agent；不使用键前缀、DC 或检查替换参数")
	ui.Text(c, hint).SingleLine().Tooltip(hint).TextColor(theme.Colors(c).Muted)
}
