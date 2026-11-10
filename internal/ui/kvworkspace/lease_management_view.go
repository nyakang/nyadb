package kvworkspace

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) leaseManagementMenu(c *ui.Context, d *Document) {
	if !d.LeaseBrowsingAvailable() {
		return
	}
	ready := !d.Busy() && !d.needsRecovery && !d.hasUnknownWrites()
	ui.Button(c, locales.Text("管理租约")).Disabled(!ready).Menu(func(m *ui.Menu) {
		if m.Item(locales.Text("授予租约")).Chosen() {
			s.OpenLeaseGrant(d)
		}
		if m.Item(locales.Text("预览撤销租约")).Chosen() {
			s.OpenLeaseRevoke(d)
		}
		detailReady := d.browserReceiptChoice == 2 && d.browserDetail != nil && d.browserDetail.Detail != nil && sameEventScope(d.browserDetail.Scope, d.scope) && d.browserDetail.Detail.Result.Reply.Lease.TTL >= 0
		if m.Item(locales.Text("预览此租约持续续租")).Disabled(!detailReady || !d.renewalAvailable).Chosen() {
			s.PrepareRenewalFromLeaseDetail(d)
		}
	})
}

func (s *State) leaseManagementModals(c *ui.Context) {
	for _, d := range s.documents {
		p := d.leaseManagement
		if !s.active(d) || p == nil || s.leave != nil {
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-lease-management-%d", d.id)), &p.open, func() {
			ui.Column(c).Width(540).Gap(8).Children(func() {
				title := locales.Text("授予租约")
				if p.kind == leaseRevokeMethod {
					title = locales.Text("预览撤销租约")
				}
				ui.Text(c, title).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), p.scope.Namespace.ProfileID, p.scope.Generation)).SingleLine()
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.Text("租约 ID")).Width(145)
					ui.TextInput(c, &p.id).Label(locales.Text("租约 ID")).Grow(1).Disabled(d.Busy())
				})
				if p.kind == leaseGrantMethod {
					ui.Text(c, locales.Text("ID 为 0 时由服务端生成；只从已确认回执接纳生成 ID")).SingleLine().Tooltip(locales.Text("ID 为 0 时由服务端生成；只从已确认回执接纳生成 ID"))
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, locales.Text("授予 TTL（秒）")).Width(145)
						ui.TextInput(c, &p.ttl).Label(locales.Text("授予 TTL（秒）")).Grow(1).Disabled(d.Busy())
					})
				} else {
					ui.Text(c, locales.Text("先重新读取此 ID 的完整关联键，再单独确认原生撤销")).SingleLine().Tooltip(locales.Text("先重新读取此 ID 的完整关联键，再单独确认原生撤销"))
				}
				if d.error != "" {
					ui.Text(c, clipped(d.error)).SingleLine().TextColor(theme.Colors(c).Error)
				}
				if d.Busy() {
					ui.Text(c, locales.Text("等待原执行器确认；不会自动重试或重放")).SingleLine()
					c.AnimationFrame()
				}
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						s.RequestCancel(d)
						s.CancelConfirmation(d)
					}
					label := locales.Text("预览授予请求")
					if p.kind == leaseRevokeMethod {
						label = locales.Text("读取撤销关联键")
					}
					if ui.Button(c, label).Disabled(d.Busy()).Clicked() {
						s.PrepareLeaseManagement(d)
					}
				})
			})
		})
		if !p.open && d.leaseManagement == p {
			s.RequestCancel(d)
			s.CancelConfirmation(d)
		}
	}
}

func (s *State) leaseManagementConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	r := p.management
	ui.Column(c).Width(540).Gap(6).Children(func() {
		ui.Text(c, locales.Text("确认 KV 写入请求")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine()
		ui.Text(c, fmt.Sprintf("%s · ID %d · TTL %d", r.Kind, r.RequestedID, r.RequestedTTL)).SingleLine().Tooltip(p.plan.Snapshot().JSON)
		ui.Text(c, locales.Text("仅派发一次；取消不代表回滚，丢失回执后必须显式核对")).SingleLine().Tooltip(locales.Text("仅派发一次；取消不代表回滚，丢失回执后必须显式核对")).TextColor(theme.Colors(c).Warning)
		if r.Observation != nil {
			ui.Text(c, locales.Text("撤销按 ID 执行；关联键可能变化，ID 可能被重新使用")).SingleLine().Tooltip(locales.Text("撤销按 ID 执行；关联键可能变化，ID 可能被重新使用")).TextColor(theme.Colors(c).Warning)
			ui.Text(c, locales.Text("没有 CAS 保护；后来 TTL 为 -1 也不能确认原未知撤销")).SingleLine().Tooltip(locales.Text("没有 CAS 保护；后来 TTL 为 -1 也不能确认原未知撤销")).TextColor(theme.Colors(c).Warning)
			l := r.Observation.Result.Reply.Lease
			ui.Text(c, fmt.Sprintf("TTL %d · GrantedTTL %d · %s", l.TTL, l.GrantedTTL, fmt.Sprintf(locales.Text("%d 个完整关联键"), len(l.Keys)))).SingleLine()
			p.keyColumns.Selected = &p.keyRow
			theme.Table(c, &p.keyColumns, []ui.TableColumn{{Title: locales.Text("字节数"), Width: 75}, {Title: locales.Text("键 Base64 预览"), Width: 445}}, len(l.Keys), func(row, col int) {
				key := l.Keys[row]
				text := strconv.Itoa(len(key))
				if col == 1 {
					text = base64.StdEncoding.EncodeToString(key[:min(len(key), 256)])
					if len(key) > 256 {
						text += "…"
					}
				}
				ui.Text(c, text).SingleLine().Tooltip(text)
			}).Label(locales.Text("撤销前关联键观察")).Height(85).FillWidth()
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制完整撤销观察")).Clicked() {
					c.WriteClipboard(managementObservationJSON(r))
				}
				if ui.Button(c, locales.Text("复制完整键 Base64")).Disabled(p.keyRow < 0 || p.keyRow >= len(l.Keys)).Clicked() {
					c.WriteClipboard(base64.StdEncoding.EncodeToString(l.Keys[p.keyRow]))
				}
			}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		} else {
			ui.Text(c, locales.Text("ID 为 0 时由服务端生成；只从已确认回执接纳生成 ID")).SingleLine().Tooltip(locales.Text("ID 为 0 时由服务端生成；只从已确认回执接纳生成 ID"))
		}
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("复制完整请求")).Clicked() {
				r := p.plan.Snapshot()
				c.WriteClipboard(r.Method + "\n" + r.JSON)
			}
			if ui.Button(c, locales.Text("保存租约预览请求")).Disabled(d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
				s.ExportLeaseManagementRequest(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		ui.Checkbox(c, &p.checked, locales.Text("我确认执行此原生写入请求"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认执行 KV 写入")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}
