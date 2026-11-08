package dataworkspace

import (
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type asyncWriteLabels struct{ verify, evidence, hint, receipt, readback, copyReceipt, copyReadback, close, keep string }

func (s *State) asyncWriteLabels(d *Document) asyncWriteLabels {
	if s.runtimeFor(d).Selector.Dialect == "qdrant" {
		return asyncWriteLabels{"核验原点结果", "批次写入证据", "批次完成不代表每个点都已更改；核验只读取原点，不会重发。提交前检查不是原子比较与交换，批次不支持回滚。", "原批次写入回执 JSON", "原点回读核验 JSON", "复制批次写入回执", "复制原点回读核验", "关闭会丢弃本地草稿与批次证据；服务器写入不会取消或回滚，原请求不会重发。请先复制所需证据。", "继续核验"}
	}
	return asyncWriteLabels{"观察任务结果", "写入任务证据", "任务受理不代表执行成功；观察只读取原任务，不会重复发送写入。提交前的记录检查不是原子比较与交换。", "原写入任务受理 JSON", "原写入任务观察 JSON", "复制写入任务受理", "复制写入任务观察", "关闭会丢弃本地草稿与任务证据；服务器任务不会取消或回滚，原请求不会重发。请先复制所需证据。", "继续观察"}
}

func (s *State) dataSaveTooltip(d *Document) string {
	if s.runtimeFor(d).AsyncWrites {
		if s.runtimeFor(d).Selector.Dialect == "qdrant" {
			return locales.Text("点批次写入：完整 point JSON 替换或删除；提交后请核验原点结果") + "\n" + locales.Text(s.asyncWriteLabels(d).hint)
		}
		return locales.Text("异步文档写入：JSON 完整替换或删除；提交后请观察任务结果") + "\n" + locales.Text(s.asyncWriteLabels(d).hint)
	}
	return locales.Text("保存数据更改")
}

// The existing native Toolbar provides keyboard navigation and overflow. Keep
// task actions here so grid footer commands cannot clip them in narrow windows.
func (s *State) asyncDataWriteActions(c *ui.Context, d *Document) {
	if d == nil || d.closed || !s.runtimeFor(d).AsyncWrites {
		return
	}
	labels := s.asyncWriteLabels(d)
	if d.edit.Uncertain != nil {
		label := locales.Text("核验提交结果")
		if d.edit.Uncertain.Async != nil {
			label = locales.Text(labels.verify)
		}
		if ui.Button(c, label).Label(label).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(d.Busy()).Clicked() {
			s.verifyMySQLDataSave(c.Services(), d)
		}
	}
	if d.edit.LastAsync != nil && theme.IconButton(c, "", "info").Label(locales.Text(labels.evidence)).Tooltip(locales.Text(labels.evidence)).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Clicked() {
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
	labels := s.asyncWriteLabels(d)
	if d.edit.AsyncCloseOpen {
		choice := ui.AlertDialog(c, &d.edit.AsyncCloseOpen, locales.Text("关闭原写入文档？"), locales.Text(labels.close), locales.Text(labels.keep), locales.Text("关闭并放弃本地草稿与证据"))
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
			ui.Text(c, locales.Text(labels.evidence)+" · "+evidence.TaskID).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, locales.Text(labels.hint))
			ui.Scroll(c).Height(280).FillWidth().Children(func() {
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, locales.Text(labels.receipt))
					ui.Text(c, asyncEvidencePreview(evidence.ReceiptJSON)).Label(locales.Text(labels.receipt))
					ui.Text(c, locales.Text(labels.readback))
					ui.Text(c, asyncEvidencePreview(evidence.TaskJSON)).Label(locales.Text(labels.readback))
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text(labels.copyReceipt)).Clicked() {
					c.WriteClipboard(evidence.ReceiptJSON)
				}
				if ui.Button(c, locales.Text(labels.copyReadback)).Disabled(evidence.TaskJSON == "").Clicked() {
					c.WriteClipboard(evidence.TaskJSON)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					d.edit.AsyncEvidenceOpen = false
				}
			})
		})
	})
}
