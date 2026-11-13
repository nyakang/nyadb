package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpAgentRequestTool(kind contract.KVRequestToolKind) bool {
	return kind == contract.KVToolHTTPAgentSelf || kind == contract.KVToolHTTPAgentServices || kind == contract.KVToolHTTPAgentChecks
}

func (s *State) httpAgentToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPAgent
	if kind != contract.KVToolHTTPAgentSelf {
		if ui.Checkbox(c, &a.IncludeFilter, locales.Text("包含原生 filter")).Disabled(disabled).Changed() && !a.IncludeFilter {
			a.Filter = ""
		}
		ui.TextInput(c, &a.Filter).Label(locales.Text("本机 Agent 原生 filter")).FillWidth().Disabled(disabled || !a.IncludeFilter)
	}
	ui.Text(c, locales.Text("读取当前连接的本机 Agent；不使用键前缀、DC 或一致性选项")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
	ui.Text(c, locales.Text("只生成 Agent 读取请求；运行后查看原响应")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
}
