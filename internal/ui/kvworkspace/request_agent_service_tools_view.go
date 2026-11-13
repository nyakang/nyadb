package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpAgentServiceRequestTool(kind contract.KVRequestToolKind) bool {
	return kind == contract.KVToolHTTPAgentServiceRegister || kind == contract.KVToolHTTPAgentServiceDeregister
}

func (s *State) httpAgentServiceToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPAgentService
	if kind == contract.KVToolHTTPAgentServiceRegister {
		ui.Scroll(c).Height(260).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				label := locales.Text("完整服务定义 JSON（保留原始字节）")
				ui.Text(c, label)
				ui.TextArea(c, &a.DefinitionJSON).Label(locales.Text("编辑完整服务定义 JSON")).Height(110).FillWidth().ReadOnly(disabled)
				if ui.Checkbox(c, &a.IncludeReplacement, locales.Text("指定原生检查替换参数")).Disabled(disabled).Changed() && !a.IncludeReplacement {
					a.ReplaceExistingChecks = ""
				}
				choice := locales.Text("空值：替换已有检查")
				if a.ReplaceExistingChecks == "true" {
					choice = locales.Text("true：替换已有检查")
				} else if a.ReplaceExistingChecks == "false" {
					choice = locales.Text("false：保留未提供的检查")
				}
				options := []string{locales.Text("空值：替换已有检查"), locales.Text("true：替换已有检查"), locales.Text("false：保留未提供的检查")}
				if ui.Select(c, &choice, options).Label(locales.Text("原生检查替换参数")).FillWidth().Disabled(disabled || !a.IncludeReplacement).Changed() {
					switch choice {
					case options[0]:
						a.ReplaceExistingChecks = ""
					case options[1]:
						a.ReplaceExistingChecks = "true"
					case options[2]:
						a.ReplaceExistingChecks = "false"
					}
				}
				ui.Text(c, locales.Text("未指定参数时保留未提供的检查；定义中的检查和 sidecar 会一同处理")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
			})
		})
	} else {
		ui.TextInput(c, &a.ID).Label(locales.Text("原生服务 ID")).FillWidth().Disabled(disabled)
		ui.Text(c, locales.Text("注销原 ID 的本机服务及关联检查和 sidecar；运行后另行确认")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
	}
	hint := locales.Text("操作当前连接的本机 Agent；不使用键前缀、DC 或一致性选项")
	ui.Text(c, hint).SingleLine().Tooltip(hint).TextColor(theme.Colors(c).Muted)
}
