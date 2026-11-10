package kvworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func renewalAcknowledged(r *RenewalReceipt) uint64 {
	if r == nil {
		return 0
	}
	if r.Batch.Ended {
		return r.Batch.Terminal.Acknowledged
	}
	count := uint64(0)
	for _, v := range r.Batch.Receipts {
		if v.Completed {
			count++
		}
	}
	return count
}
func (s *State) renewalReceiptView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			r := d.renewalReceipt
			if r == nil {
				ui.Text(c, locales.Text("确认持续续租后显示原始回执")).TextColor(p.Muted)
				return
			}
			ui.Text(c, fmt.Sprintf(locales.Text("回执连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(p.Muted)
			ui.Text(c, fmt.Sprintf(locales.Text("原租约 ID：%d"), r.Batch.Request.LeaseID)).SingleLine().Bold()
			status := locales.Text("等待首次续租确认")
			if renewalAcknowledged(r) > 0 {
				status = locales.Text("正在持续续租原租约")
			}
			if r.Batch.Ended {
				status = locales.Text("原持续续租已停止")
			}
			if r.Batch.Terminal.WriteOutcomeUnknown {
				status = locales.Text("原续租未确认；此文档禁止继续写入")
			}
			ui.Text(c, status).Bold().SingleLine().TextColor(p.Text)
			ui.Text(c, fmt.Sprintf(locales.Text("已确认续租：%d · 完整回执：%d"), renewalAcknowledged(r), len(r.Batch.Receipts))).SingleLine().TextColor(p.Muted)
			if len(r.Batch.Receipts) > 0 {
				latest := r.Batch.Receipts[len(r.Batch.Receipts)-1]
				if latest.Completed {
					ui.Text(c, fmt.Sprintf(locales.Text("最近确认 TTL：%d 秒 · 修订号：%d"), latest.TTL, latest.Header.Revision)).SingleLine()
				}
				ui.Text(c, fmt.Sprintf(locales.Text("原成员：%d · 原集群：%d"), r.Scope.Header.MemberID, r.Scope.Header.ClusterID)).SingleLine().TextColor(p.Muted)
			}
			if r.Batch.Terminal.CaptureIncomplete {
				ui.Text(c, locales.Text("原始回执捕获不完整")).SingleLine().TextColor(p.Warning)
			}
			if r.Batch.Ended && r.Batch.Terminal.Reason != "" {
				ui.Text(c, r.Batch.Terminal.Reason+" · "+r.Batch.Terminal.Code).SingleLine().Tooltip(clipped(r.Batch.Terminal.Message)).TextColor(p.Muted)
			}
			if r.Error != "" {
				ui.Text(c, clipped(r.Error)).SingleLine().Tooltip(clipped(r.Error)).TextColor(p.Warning)
			}
			ui.Text(c, locales.Text("停止续租后，原租约及关联键可能自然过期")).FontSize(theme.Font(c, 11)).SingleLine().TextColor(p.Warning)
			ui.Text(c, locales.Text("预览最近 32 次回执；复制与导出保留全部原始内容")).FontSize(theme.Font(c, 11)).SingleLine().Tooltip(locales.Text("预览最近 32 次回执；复制与导出保留全部原始内容")).TextColor(p.Muted)
			preview := s.RenewalPreview(d)
			area := ui.TextArea(c, &preview).Label(locales.Text("KV 持续续租回执")).ReadOnly(true).FillWidth().Height(220)
			theme.BindNativeEdit(c, area, fmt.Sprintf("kv/renewal/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
		})
	})
}
func (s *State) renewalModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.renewalConfirmation == nil || s.leave != nil {
			continue
		}
		confirm := d.renewalConfirmation
		theme.Modal(c.Key(fmt.Sprintf("kv-renewal-confirm-%d", d.id)), &confirm.open, func() {
			ui.Column(c).Width(500).Gap(8).Children(func() {
				ui.Text(c, locales.Text("确认持续续租")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), confirm.scope.Namespace.ProfileID, confirm.scope.Generation)).SingleLine()
				ui.Text(c, fmt.Sprintf(locales.Text("原租约 ID：%d"), confirm.plan.Snapshot().LeaseID)).Bold().SingleLine()
				ui.Text(c, locales.Text("确认后，将按服务端实际 TTL 持续维护此原租约，直到明确停止或运行结束。")).Wrap().FillWidth()
				ui.Text(c, locales.Text("停止续租后，原租约及关联键可能自然过期")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
				ui.Text(c, locales.Text("恢复连接不会重新开始续租；新的续租需要再次确认。")).Wrap().FillWidth()
				preview := clipped(confirm.authored.JSON)
				ui.TextArea(c, &preview).Label(locales.Text("原持续续租请求")).ReadOnly(true).Height(90).FillWidth()
				if ui.Button(c, locales.Text("复制完整请求")).Clicked() {
					c.WriteClipboard(confirm.authored.Method + "\n" + confirm.authored.JSON)
				}
				ui.Checkbox(c, &confirm.checked, locales.Text("我授权持续续租此原租约"))
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						s.CancelConfirmation(d)
					}
					if ui.Button(c, locales.Text("确认开始持续续租")).Disabled(!confirm.checked || d.Busy()).Clicked() {
						s.ConfirmRenewal(d, confirm.checked)
					}
				})
			})
		})
		if !confirm.open && d.renewalConfirmation == confirm {
			s.CancelConfirmation(d)
		}
	}
}
