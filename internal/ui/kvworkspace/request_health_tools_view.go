package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpHealthRequestTool(kind contract.KVRequestToolKind) bool {
	switch kind {
	case contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService:
		return true
	}
	return false
}

func (s *State) httpHealthToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput, field func(string, *string)) {
	h := &input.HTTPHealth
	disabled := d.Busy() || d.FileBusy()
	hint := func(text string) { ui.Text(c, text).SingleLine().Tooltip(text).TextColor(theme.Colors(c).Muted) }
	ui.Scroll(c).Height(250).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			switch kind {
			case contract.KVToolHTTPHealthState:
				ui.Select(c, &h.State, []string{"any", "passing", "warning", "critical"}).Label(locales.Text("原生健康状态")).FillWidth().Disabled(disabled)
			case contract.KVToolHTTPHealthNode:
				field(locales.Text("原节点名称"), &h.Node)
			case contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService:
				field(locales.Text("原服务名称"), &h.Service)
			}
			if kind == contract.KVToolHTTPHealthService {
				options := []string{locales.Text("passing 未指定"), "passing=false", "passing=true"}
				choice := options[0]
				if h.Passing == "false" {
					choice = options[1]
				}
				if h.Passing == "true" {
					choice = options[2]
				}
				if ui.Select(c, &choice, options).Label(locales.Text("服务实例 passing 参数")).FillWidth().Disabled(disabled).Changed() {
					h.Passing = ""
					if choice == options[1] {
						h.Passing = "false"
					}
					if choice == options[2] {
						h.Passing = "true"
					}
				}
			}
			optional := func(label string, included *bool, value *string) {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if ui.Checkbox(c, included, label).Width(150).Disabled(disabled).Changed() && !*included {
						*value = ""
					}
					ui.TextInput(c, value).Label(label + " " + locales.Text("字段值")).Grow(1).MinWidth(0).Disabled(disabled || !*included)
				})
			}
			optional(locales.Text("包含原生 filter"), &h.IncludeFilter, &h.Filter)
			optional(locales.Text("包含原生 near"), &h.IncludeNear, &h.Near)
			field("index (uint64)", &h.Index)
			field("wait", &h.Wait)
			hint(locales.Text("index 留空表示省略，0 为显式值；wait 需要 index，最长 10 分钟"))
			hint(locales.Text("健康查询使用原连接的 DC 与一致性，不受键前缀限制"))
			hint(locales.Text("只生成健康读取请求；运行后浏览原响应"))
		})
	})
}
