package kvworkspace

import (
	"encoding/base64"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func eventWatchCreated(r *EventReceipt) bool {
	if r != nil && r.Watch != nil {
		for _, frame := range r.Batch.Frames {
			if frame.Created && !frame.Canceled && frame.WatchID == r.Watch.WatchID {
				return true
			}
		}
	}
	return false
}
func (s *State) eventReceiptView(c *ui.Context, d *Document) {
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() { s.eventReceiptContents(c, d) })
	})
}
func (s *State) eventReceiptContents(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	r := d.eventReceipt
	if r == nil {
		ui.Text(c, locales.Text("启动订阅或读取历史后显示事件回执")).TextColor(p.Muted)
		return
	}
	ui.Text(c, fmt.Sprintf(locales.Text("回执连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(p.Muted)
	key := []byte(nil)
	if r.Watch != nil {
		key = r.Watch.Key
		state := locales.Text("正在建立原订阅")
		if eventWatchCreated(r) {
			state = locales.Text("正在观察原键事件")
		}
		if r.Batch.Ended {
			state = locales.Text("原订阅已停止")
			if r.Batch.Incomplete {
				state = locales.Text("原订阅已结束，回执不完整")
			}
		}
		ui.Text(c, state).Bold().TextColor(p.Text)
		ui.Text(c, fmt.Sprintf(locales.Text("起始修订号：%d · Watch ID：%d"), r.Watch.StartRevision, r.Watch.WatchID)).SingleLine()
		count := 0
		for _, frame := range r.Batch.Frames {
			count += len(frame.Events)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("已捕获事件：%d · 原始帧：%d"), count, len(r.Batch.Frames))).SingleLine().TextColor(p.Muted)
		for i := len(r.Batch.Frames) - 1; i >= 0; i-- {
			if frame := r.Batch.Frames[i]; frame.Progress {
				ui.Text(c, fmt.Sprintf(locales.Text("最近进度修订号：%d"), frame.Header.Revision)).SingleLine().TextColor(p.Muted)
				break
			}
		}
		if r.Batch.Terminal.Reason != "" {
			ui.Text(c, r.Batch.Terminal.Reason+" · "+r.Batch.Terminal.Code).SingleLine().Tooltip(clipped(r.Batch.Terminal.Message)).TextColor(p.Muted)
		}
	}
	if h := r.History; h != nil {
		key = h.Authored.Key
		state := locales.Text("历史窗口尚未确认")
		if h.Complete {
			state = locales.Text("固定历史窗口已确认")
		}
		ui.Text(c, state).Bold().TextColor(p.Text)
		ui.Text(c, fmt.Sprintf(locales.Text("固定窗口：%d → %d"), h.Window.StartRevision, h.Window.EndRevision)).SingleLine()
		ui.Text(c, fmt.Sprintf(locales.Text("显示窗口事件：%d · 原始帧：%d"), len(h.Events), len(h.Frames))).SingleLine().TextColor(p.Muted)
		ui.Text(c, fmt.Sprintf(locales.Text("进度证明修订号：%d · 请求次数：%d"), h.ProgressHeader.Revision, h.ProgressRequests)).SingleLine().TextColor(p.Muted)
		if h.Truncated {
			ui.Text(c, locales.Text("事件列表已限制；记录保留已捕获的原始帧")).SingleLine().TextColor(p.Warning)
		}
		if h.CaptureIncomplete {
			ui.Text(c, locales.Text("原始回执捕获不完整")).SingleLine().TextColor(p.Warning)
		}
		if h.Terminal.Reason != "" {
			ui.Text(c, h.Terminal.Reason+" · "+h.Terminal.Code).SingleLine().Tooltip(clipped(h.Terminal.Message)).TextColor(p.Muted)
		}
	} else if r.Watch == nil {
		ui.Text(c, locales.Text("正在读取固定历史窗口")).Bold().TextColor(p.Muted)
	}
	if len(key) > 0 {
		encoded := base64.StdEncoding.EncodeToString(key)
		preview := encoded[:min(len(encoded), 256)]
		if len(encoded) > 256 {
			preview += "…"
		}
		ui.Row(c).FillWidth().Gap(5).Children(func() {
			ui.Text(c, "Key (Base64): "+preview).SingleLine().MinWidth(0).Grow(1).Tooltip(encoded[:min(len(encoded), 1024)])
			if ui.Button(c, locales.Text("复制完整原键")).Clicked() {
				c.WriteClipboard(encoded)
			}
		})
	}
	if r.Error != "" {
		ui.Text(c, clipped(r.Error)).SingleLine().Tooltip(clipped(r.Error)).TextColor(p.Warning)
	}
	ui.Text(c, locales.Text("列表最多 500 条，字段预览 1024 字节；复制与导出保留原始回执")).FontSize(theme.Font(c, 11)).SingleLine().TextColor(p.Muted).Tooltip(locales.Text("列表最多 500 条，字段预览 1024 字节；复制与导出保留原始回执"))
	preview := s.EventPreview(d)
	area := ui.TextArea(c, &preview).Label(locales.Text("KV 事件回执")).ReadOnly(true).FillWidth().Height(220)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/events/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}
