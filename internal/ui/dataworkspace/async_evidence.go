package dataworkspace

import (
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) dataSaveTooltip(d *Document) string {
	if s.runtimeFor(d).AsyncWrites {
		return locales.Text("异步文档写入：JSON 完整替换或删除；提交后请观察任务结果") + "\n" + locales.Text("任务受理不代表执行成功；观察只读取原任务，不会重复发送写入。提交前的记录检查不是原子比较与交换。")
	}
	return locales.Text("保存数据更改")
}

// The existing native Toolbar provides keyboard navigation and overflow. Keep
// task actions here so grid footer commands cannot clip them in narrow windows.
func (s *State) asyncDataWriteActions(c *ui.Context, d *Document) {
	if d == nil || d.closed || !s.runtimeFor(d).AsyncWrites {
		return
	}
	if d.edit.Uncertain != nil {
		label := locales.Text("核验提交结果")
		if d.edit.Uncertain.Async != nil {
			label = locales.Text("观察任务结果")
		}
		if ui.Button(c, label).Label(label).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(d.Busy()).Clicked() {
			s.verifyMySQLDataSave(c.Services(), d)
		}
	}
	if d.edit.LastAsync != nil && theme.IconButton(c, "", "info").Label(locales.Text("写入任务证据")).Tooltip(locales.Text("写入任务证据")).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Clicked() {
		d.edit.AsyncEvidenceOpen = true
	}
	if d.edit.Uncertain != nil && s.deps.CloseDocument != nil && theme.IconButton(c, "", "close").Label(locales.Text("关闭待核验文档")).Tooltip(locales.Text("关闭待核验文档")).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Disabled(d.Busy()).Clicked() {
		d.edit.AsyncCloseOpen = true
	}
	if d.edit.Uncertain != nil || d.edit.LastAsync != nil {
		ui.Divider(c).Width(1).Height(18)
	}
}

func asyncEvidencePreview(raw string) string {
	if len(raw) <= 64<<10 {
		return raw
	}
	raw = raw[:64<<10]
	for !utf8.ValidString(raw) {
		raw = raw[:len(raw)-1]
	}
	return raw + "\n… " + locales.Text("任务证据预览已截断；可复制完整内容")
}

func (s *State) asyncDataEvidenceModal(c *ui.Context, d *Document) {
	if d == nil || d.closed {
		return
	}
	if d.edit.AsyncCloseOpen {
		choice := ui.AlertDialog(c, &d.edit.AsyncCloseOpen, locales.Text("关闭原写入文档？"), locales.Text("关闭会丢弃本地草稿与任务证据；服务器任务不会取消或回滚，原请求不会重发。请先复制所需证据。"), locales.Text("继续观察"), locales.Text("关闭并放弃本地草稿与证据"))
		if choice == 1 && !d.Busy() && s.deps.CloseDocument != nil {
			d.edit.AsyncCloseApproved = true
			s.deps.CloseDocument(c.Services(), d)
			d.edit.AsyncCloseApproved = false
		}
	}
	if d.closed || !d.edit.AsyncEvidenceOpen || d.edit.LastAsync == nil {
		return
	}
	evidence := *d.edit.LastAsync
	theme.Modal(c, &d.edit.AsyncEvidenceOpen, func() {
		ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("写入任务证据")+" · "+evidence.TaskID).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, locales.Text("任务受理不代表执行成功；观察只读取原任务，不会重复发送写入。提交前的记录检查不是原子比较与交换。"))
			ui.Scroll(c).Height(280).FillWidth().Children(func() {
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, asyncEvidencePreview(evidence.ReceiptJSON)).Label(locales.Text("原写入任务受理 JSON"))
					ui.Text(c, asyncEvidencePreview(evidence.TaskJSON)).Label(locales.Text("原写入任务观察 JSON"))
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("复制写入任务受理")).Clicked() {
					c.WriteClipboard(evidence.ReceiptJSON)
				}
				if ui.Button(c, locales.Text("复制写入任务观察")).Disabled(evidence.TaskJSON == "").Clicked() {
					c.WriteClipboard(evidence.TaskJSON)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					d.edit.AsyncEvidenceOpen = false
				}
			})
		})
	})
}
