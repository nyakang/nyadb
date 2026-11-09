package dataworkspace

import (
	"context"
	"io"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// One latest original transaction receipt is retained per document. Export
// captures its immutable bytes before opening a picker or waiting on the disk.
// Receipt presentation cannot change write acknowledgement or enable replay.
type nativeWriteReceiptState struct {
	JSON                     string
	Open                     bool
	CloseOpen, CloseApproved bool
	Cancel                   context.CancelFunc
	Results                  <-chan error
	Err                      string
}

func nativeWriteReceiptPreview(raw string) string {
	if len(raw) <= 64<<10 {
		return raw
	}
	raw = raw[:64<<10]
	for !utf8.ValidString(raw) {
		raw = raw[:len(raw)-1]
	}
	return raw + "\n… " + locales.Text("回执预览已截断；可复制或导出完整内容")
}

func (s *State) nativeWriteReceiptActions(c *ui.Context, d *Document) {
	if d == nil || d.closed {
		return
	}
	if d.nativeReceipt.JSON != "" && theme.IconButton(c, "", "info").Label(locales.Text("原生事务回执")).Tooltip(locales.Text("原生事务回执")).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Clicked() {
		d.nativeReceipt.Open = true
	}
	canClose := s.runtimeFor(d).NativeReceipts && d.edit.Uncertain != nil && s.deps.CloseDocument != nil
	if canClose && theme.IconButton(c, "", "close").Label(locales.Text("关闭原写入文档")).Tooltip(locales.Text("关闭原写入文档")).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Disabled(d.Busy()).Clicked() {
		d.nativeReceipt.CloseOpen = true
	}
	if canClose || d.nativeReceipt.JSON != "" {
		ui.Divider(c).Width(1).Height(18)
	}
}

func (s *State) nativeWriteReceiptModal(c *ui.Context, d *Document) {
	if d == nil || d.closed {
		return
	}
	if d.nativeReceipt.CloseOpen {
		choice := ui.AlertDialog(c, &d.nativeReceipt.CloseOpen, locales.Text("关闭原写入文档？"), locales.Text("关闭会丢弃本地草稿与原生回执；不会重新发送写入，也不会确认或回滚原事务。请先复制或导出所需回执。"), locales.Text("取消"), locales.Text("关闭并放弃本地草稿与证据"))
		if choice == 1 {
			s.closeNativeWriteDocument(c.Services(), d)
		}
	}
	if d.closed || !d.nativeReceipt.Open || d.nativeReceipt.JSON == "" {
		return
	}
	receipt := d.nativeReceipt.JSON
	theme.Modal(c, &d.nativeReceipt.Open, func() {
		ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("原生事务回执")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, locales.Text("保留最近一次写入的原始回执；下一次回执会替换它。语句结果在事务提交前属于临时结果。"))
			ui.Scroll(c).Height(280).FillWidth().Children(func() {
				ui.Text(c, nativeWriteReceiptPreview(receipt)).Label(locales.Text("原生事务回执 JSON"))
			})
			if d.nativeReceipt.Err != "" {
				ui.Text(c, d.nativeReceipt.Err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
					c.WriteClipboard(receipt)
				}
				if ui.Button(c, locales.Text("导出完整回执")).Disabled(d.nativeReceipt.Results != nil).Clicked() {
					s.exportNativeWriteReceipt(c.Services(), d, mygo.Dialog.Save)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					d.nativeReceipt.Open = false
				}
			})
		})
	})
}

func (s *State) closeNativeWriteDocument(services ui.Services, d *Document) {
	if d == nil || d.closed || d.Busy() || d.edit.Uncertain == nil || !s.runtimeFor(d).NativeReceipts || s.deps.CloseDocument == nil {
		return
	}
	d.nativeReceipt.CloseApproved = true
	s.deps.CloseDocument(services, d)
	d.nativeReceipt.CloseApproved = false
}

func (s *State) exportNativeWriteReceipt(services ui.Services, d *Document, picker func(mygo.SaveDialogOptions) (string, error)) {
	if d == nil || d.closed || d.nativeReceipt.JSON == "" || d.nativeReceipt.Results != nil {
		return
	}
	receipt := d.nativeReceipt.JSON
	ctx, cancel := context.WithCancel(context.Background())
	d.nativeReceipt.Cancel, d.nativeReceipt.Err = cancel, ""
	out := make(chan error, 1)
	d.nativeReceipt.Results = out
	go func() {
		path, err := picker(mygo.SaveDialogOptions{Title: locales.Text("导出完整回执"), DefaultPath: "transaction-receipt.json", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
		if err == nil && path != "" {
			err = mysqlDataAtomicFile(ctx, path, func(w io.Writer) error { _, e := io.WriteString(w, receipt); return e })
		}
		out <- err
		services.Invalidate()
	}()
}

func (s *State) consumeNativeWriteReceipt(d *Document) {
	if d.nativeReceipt.Results == nil {
		return
	}
	select {
	case err := <-d.nativeReceipt.Results:
		d.nativeReceipt.Cancel()
		d.nativeReceipt.Cancel, d.nativeReceipt.Results = nil, nil
		if err != nil {
			d.nativeReceipt.Err = err.Error()
		}
	default:
	}
}
