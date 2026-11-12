package kvworkspace

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func requestToolDisplayError(value string) string {
	for _, source := range []string{"KV 请求文本超过 16 MiB", "请求文本已更改，请重新打开工具后生成", "KV 请求无效"} {
		for _, language := range []string{"zh-Hans", "en"} {
			if value == locales.TextIn(language, source) {
				return locales.Text(source)
			}
		}
	}
	for _, source := range []string{"KV 请求无效", "KV 文件操作失败"} {
		for _, language := range []string{"zh-Hans", "en"} {
			prefix := locales.TextIn(language, source) + ": "
			if strings.HasPrefix(value, prefix) {
				return locales.Text(source) + ": " + strings.TrimPrefix(value, prefix)
			}
		}
	}
	return value
}

func httpSessionRequestTool(kind contract.KVRequestToolKind) bool {
	switch kind {
	case contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease:
		return true
	}
	return false
}

func (s *State) httpSessionToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput, field func(string, *string)) {
	disabled := d.Busy() || d.FileBusy()
	hint := func(value string) { ui.Text(c, value).SingleLine().Tooltip(value).TextColor(theme.Colors(c).Muted) }
	if kind == contract.KVToolHTTPSessionCreate {
		ui.Scroll(c).Height(260).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				optional := func(label string, included *bool, value *string) {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						if ui.Checkbox(c, included, label).Width(150).Disabled(disabled).Changed() && !*included {
							*value = ""
						}
						ui.TextInput(c, value).Label(label + " " + locales.Text("字段值")).Grow(1).MinWidth(0).Disabled(disabled || !*included)
					})
				}
				optional(locales.Text("包含 Session 名称"), &input.HTTPSession.IncludeName, &input.HTTPSession.Name)
				optional(locales.Text("包含 Session 节点"), &input.HTTPSession.IncludeNode, &input.HTTPSession.Node)
				field("TTL", &input.HTTPSession.TTL)
				field("LockDelay", &input.HTTPSession.LockDelay)
				field("Behavior", &input.HTTPSession.Behavior)
				hint(locales.Text("名称/节点勾选后可显式留空；TTL、LockDelay、Behavior 留空表示省略"))
				hint(locales.Text("TTL 10s 至 24h；LockDelay 0s 至 60s；Behavior 为 release 或 delete"))
				ui.Text(c, locales.Text("NodeChecks JSON（留空省略，[] 为无检查）"))
				ui.TextArea(c, &input.HTTPSession.NodeChecksJSON).Label(locales.Text("NodeChecks JSON（留空省略，[] 为无检查）")).Height(50).FillWidth().ReadOnly(disabled)
			})
		})
		return
	}
	field(locales.Text("Session UUID"), &input.HTTPSession.ID)
	if kind == contract.KVToolHTTPSessionInfo {
		hint(locales.Text("读取指定原 UUID；后续观察不会确认此前未知写入"))
		return
	}
	if kind == contract.KVToolHTTPSessionDestroy {
		hint(locales.Text("销毁此原 Session；与停止续租或释放单个键不同"))
		return
	}
	field(locales.Text("绝对键"), &input.Key)
	hint(locales.Text("HTTP 锁请求拒绝空路径段及 . 或 ..；不会改写键或转换为事务"))
	ui.Text(c, locales.Text("原值 Base64（空文本表示空字节值）"))
	ui.TextArea(c, &input.ValueBase64).Label(locales.Text("原值 Base64（空文本表示空字节值）")).Height(55).FillWidth().ReadOnly(disabled)
	field("Flags (uint64)", &input.Flags)
	hint(locales.Text("Flags 留空表示省略；0 为显式值，使用完整十进制"))
	hint(locales.Text("原生 true/false 仅确认本次请求，不保证持续持锁"))
}
