package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpAgentMaintenanceRequestTool(kind contract.KVRequestToolKind) bool {
	return kind == contract.KVToolHTTPAgentNodeMaintenance || kind == contract.KVToolHTTPAgentServiceMaintenance
}

func (s *State) httpAgentMaintenanceToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPAgentMaintenance
	if kind == contract.KVToolHTTPAgentServiceMaintenance {
		ui.Text(c, locales.Text("原生服务 ID"))
		ui.TextInput(c, &a.ID).Label(locales.Text("输入原生服务 ID")).FillWidth().Disabled(disabled)
	}
	ui.Text(c, locales.Text("enable：true 启用维护，false 禁用维护"))
	ui.TextInput(c, &a.Enable).Label(locales.Text("原生 enable 布尔值")).FillWidth().Disabled(disabled)
	ui.Checkbox(c, &a.IncludeReason, locales.Text("显式包含 reason（省略或空值使用原生默认原因）")).Disabled(disabled)
	ui.TextArea(c, &a.ReasonJSON).Label(locales.Text("原始 reason JSON 字符串")).Height(85).FillWidth().ReadOnly(disabled || !a.IncludeReason)
	for _, text := range []string{"重复启用保留原原因；禁用只移除维护检查", "使用 JSON 字符串保留原因的换行和控制字符", "成功回执确认本机操作；目录健康状态需要另行读取"} {
		hint := locales.Text(text)
		ui.Text(c, hint).SingleLine().Tooltip(hint).TextColor(theme.Colors(c).Muted)
	}
}
