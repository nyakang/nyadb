package kvworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func requestToolName(kind contract.KVRequestToolKind) string {
	switch kind {
	case contract.KVToolRead:
		return locales.Text("读取键")
	case contract.KVToolList:
		return locales.Text("列出前缀")
	case contract.KVToolRaw:
		return locales.Text("读取原始值")
	case contract.KVToolPut:
		return locales.Text("写入键")
	case contract.KVToolDelete:
		return locales.Text("删除键或前缀")
	case contract.KVToolTransaction:
		return locales.Text("原生 KV 事务")
	case contract.KVToolHTTPSessionInfo:
		return locales.Text("读取 HTTP Session")
	case contract.KVToolHTTPSessionList:
		return locales.Text("列出 HTTP Sessions")
	case contract.KVToolHTTPNodeSessions:
		return locales.Text("列出节点 Sessions")
	case contract.KVToolHTTPSessionCreate:
		return locales.Text("创建 HTTP Session")
	case contract.KVToolHTTPSessionDestroy:
		return locales.Text("销毁 HTTP Session")
	case contract.KVToolHTTPAcquire:
		return locales.Text("获取 HTTP Session 锁")
	case contract.KVToolHTTPRelease:
		return locales.Text("释放 HTTP Session 锁")
	}
	return string(kind)
}

func (s *State) requestToolsMenu(c *ui.Context, d *Document) {
	tools := d.RequestTools()
	if len(tools) == 0 {
		return
	}
	ui.Button(c, locales.Text("KV 请求工具")).Disabled(d.Busy() || d.FileBusy()).Menu(func(m *ui.Menu) {
		for _, tool := range tools {
			if m.Item(requestToolName(tool.Kind)).Chosen() {
				s.OpenRequestTool(d, tool.Kind)
			}
		}
	})
}

func (s *State) requestToolsModals(c *ui.Context) {
	for _, d := range s.documents {
		p := d.requestTools
		if !s.active(d) || p == nil || !p.open || s.leave != nil {
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-request-tool-%d", d.id)), &p.open, func() {
			ui.Column(c).Width(560).Gap(7).Children(func() {
				ui.Text(c, locales.Text("KV 请求工具")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), d.profileID, d.scope.Generation)).SingleLine()
				choice := requestToolName(p.kind)
				options := []string{}
				for _, tool := range d.RequestTools() {
					options = append(options, requestToolName(tool.Kind))
				}
				if ui.Select(c, &choice, options).Label(locales.Text("请求工具操作")).FillWidth().Disabled(d.Busy() || d.FileBusy()).Changed() {
					for _, tool := range d.RequestTools() {
						if requestToolName(tool.Kind) == choice {
							s.OpenRequestTool(d, tool.Kind)
						}
					}
				}
				input := p.drafts[p.kind].input
				ui.Text(c, locales.Text("只生成请求文本；运行后另行确认写入")).SingleLine().Tooltip(locales.Text("只生成请求文本；运行后另行确认写入")).TextColor(theme.Colors(c).Muted)
				field := func(label string, value *string) {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, label).Width(150)
						ui.TextInput(c, value).Label(label).Grow(1).MinWidth(0).Disabled(d.Busy() || d.FileBusy())
					})
				}
				if httpSessionRequestTool(p.kind) {
					s.httpSessionToolInputs(c, d, p.kind, &input, field)
				} else {
					if p.kind == contract.KVToolTransaction {
						ui.Text(c, locales.Text("事务 JSON（保留原始字节）"))
						ui.TextArea(c, &input.TransactionJSON).Label(locales.Text("事务 JSON（保留原始字节）")).Height(160).FillWidth().ReadOnly(d.Busy() || d.FileBusy())
					} else {
						field(locales.Text("绝对键或前缀"), &input.Key)
						ui.Text(c, locales.Text("前缀仅为初始建议，不会自动拼接；特殊路径使用原生事务")).SingleLine().Tooltip(locales.Text("前缀仅为初始建议，不会自动拼接；特殊路径使用原生事务")).TextColor(theme.Colors(c).Muted)
					}
					if p.kind == contract.KVToolRaw {
						ui.Text(c, locales.Text("原始值 GET 拒绝空路径段及 . 或 ..，避免服务端改写键")).SingleLine().Tooltip(locales.Text("原始值 GET 拒绝空路径段及 . 或 ..，避免服务端改写键"))
					}
					if p.kind == contract.KVToolList {
						ui.Checkbox(c, &input.KeysOnly, locales.Text("仅请求键名（特殊路径保留完整事务条目）")).Disabled(d.Busy() || d.FileBusy())
					}
					if p.kind == contract.KVToolPut {
						ui.Text(c, locales.Text("原值 Base64（空文本表示空字节值）"))
						ui.TextArea(c, &input.ValueBase64).Label(locales.Text("原值 Base64（空文本表示空字节值）")).Height(65).FillWidth().ReadOnly(d.Busy() || d.FileBusy())
						field("Flags (uint64)", &input.Flags)
					}
					if p.kind == contract.KVToolPut || p.kind == contract.KVToolDelete {
						field("CAS (uint64)", &input.CompareIndex)
						ui.Text(c, locales.Text("Flags / CAS 留空表示省略；0 为显式值，使用完整十进制")).SingleLine().Tooltip(locales.Text("Flags / CAS 留空表示省略；0 为显式值，使用完整十进制")).TextColor(theme.Colors(c).Muted)
					}
					if p.kind == contract.KVToolDelete {
						ui.Checkbox(c, &input.Recursive, locales.Text("递归删除此前缀（不能与 CAS 同用）")).Disabled(d.Busy() || d.FileBusy())
					}
				}
				s.SetRequestToolInput(d, p.kind, input)
				if d.error != "" {
					message := requestToolDisplayError(d.error)
					ui.Text(c, clipped(message)).SingleLine().Tooltip(clipped(message)).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制工具输入")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
						s.CopyRequestToolInput(c.Services(), d)
					}
					if ui.Button(c, locales.Text("导出工具输入")).Disabled(d.Busy() || d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
						s.ExportRequestToolInput(d)
					}
				})
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("返回请求文本")).Clicked() {
						p.open = false
					}
					if ui.Button(c, locales.Text("生成请求文本")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
						s.GenerateRequestTool(d)
					}
				})
			})
		})
	}
}
