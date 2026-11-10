package kvworkspace

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func clipped(text string) string {
	if len(text) <= previewLimit {
		return text
	}
	end := previewLimit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "\n…"
}
func (s *State) View(c *ui.Context, d *Document) {
	if !s.owned(d) {
		return
	}
	p := theme.Colors(c)
	availableMethods := s.MethodsFor(d)
	isEvent := eventMethod(d.method)
	isRenewal := d.method == RenewalMethod
	isBrowser := leaseBrowserMethod(d.method)
	isAuth := d.method == AuthManagementMethod
	ready := !d.Busy() && !d.NeedsRecovery() && (!isEvent && !isRenewal && d.lease != nil || isEvent && d.eventsAvailable || isRenewal && d.renewalAvailable)
	ui.Column(c).Fill().Background(p.Panel).Children(func() {
		ui.Row(c).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Background(p.Header).Children(func() {
			ui.Toolbar(c, func() {
				runLabel, runID := locales.Text("运行请求"), locales.Text("运行 KV 请求")
				if isAuth {
					runLabel, runID = locales.Text("刷新权限目录"), locales.Text("刷新权限目录")
				}
				if isBrowser {
					runLabel = locales.Text("读取租约观察")
					runID = runLabel
				}
				if isRenewal {
					runLabel = locales.Text("开始持续续租")
					runID = runLabel
				}
				if d.method == WatchMethod {
					runLabel = locales.Text("启动 Watch")
					runID = runLabel
				} else if d.method == HistoryMethod {
					runLabel = locales.Text("读取历史")
					runID = runLabel
				}
				if ui.Button(c, runLabel).Label(runID).Disabled(!ready || isAuth && !d.AuthManagementAvailable() || isBrowser && !d.LeaseBrowsingAvailable() || isEvent && d.eventReceipt != nil || isRenewal && (d.renewalReceipt != nil || d.hasUnknownWrites())).Clicked() {
					s.Run(d)
				}
				if d.method == WatchMethod {
					if ui.Button(c, locales.Text("请求进度")).Disabled(d.eventJob == nil || d.eventJob.kind != "watch" || d.CancelRequested() || !eventWatchCreated(d.eventReceipt)).Clicked() {
						s.ProgressEvents(d)
					}
					if ui.Button(c, locales.Text("停止 Watch")).Disabled(d.eventJob == nil || d.eventJob.kind != "watch" || d.CancelRequested()).Clicked() {
						s.StopEvents(d)
					}
				}
				if isRenewal {
					if ui.Button(c, locales.Text("停止持续续租")).Disabled(d.renewalJob == nil || d.renewalJob.kind != "renew" || d.CancelRequested()).Clicked() {
						s.StopRenewal(d)
					}
					if ui.Button(c, locales.Text("清除续租回执")).Disabled(!ready || d.renewalReceipt == nil || !d.renewalReceipt.Batch.Ended || d.FileBusy() || d.hasUnknownWrites()).Clicked() {
						s.ClearRenewalReceipt(d)
					}
				}
				if isEvent && ui.Button(c, locales.Text("清除事件回执")).Disabled(!ready || d.eventReceipt == nil || d.FileBusy()).Clicked() {
					s.ClearEventReceipt(d)
				}
				if ui.Button(c, locales.Text("取消")).Disabled(!d.Busy() || d.CancelRequested()).Clicked() {
					s.RequestCancel(d)
				}
				if ui.Button(c, locales.Text("预连接")).Disabled(!ready || isEvent && d.eventReceipt != nil || isRenewal && d.renewalReceipt != nil).Clicked() {
					s.Preconnect(d)
				}
				if ui.Button(c, locales.Text("打开请求文本")).Disabled(d.Busy() || d.FileBusy() || s.deps.Files.OpenPath == nil).Clicked() {
					s.OpenRequest(d)
				}
				if ui.Button(c, locales.Text("保存请求")).Disabled(d.FileBusy() || s.deps.Files.WriteStream == nil).Clicked() {
					s.SaveRequest(d, false)
				}
				if ui.Button(c, locales.Text("另存为")).Disabled(d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
					s.SaveRequest(d, true)
				}
				if d.FileBusy() && ui.Button(c, locales.Text("取消文件操作")).Clicked() {
					s.CancelFile(d)
				}
			}).Label(locales.Text("KV 请求操作")).Grow(1).MinWidth(0).Padding(3).Gap(5)
		})
		ui.Row(c).Padding(5, 8).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
			options := []string{}
			choice := d.method
			for _, m := range availableMethods {
				options = append(options, m.Name)
				if m.Path == d.method {
					choice = m.Name
				}
			}
			if ui.Select(c, &choice, options).Label(locales.Text("KV 方法")).Width(165).Disabled(d.Busy()).Changed() {
				for _, m := range availableMethods {
					if m.Name == choice {
						s.ChooseMethod(d, m.Path)
					}
				}
			}
			if (isEvent || isRenewal || isBrowser || isAuth) && ui.Button(c, locales.Text("使用请求模板")).Disabled(d.Busy()).Clicked() {
				request := DefaultEventRequest(d.method)
				if isAuth {
					request = DefaultAuthManagementRequest()
				}
				if isRenewal {
					request = DefaultRenewalRequest()
				}
				if isBrowser {
					request = DefaultLeaseBrowserRequest(d.method)
				}
				d.Editor.Buffer.Set(request.JSON)
				d.permit = nil
			}
			name := fmt.Sprintf(locales.Text("连接 #%d"), d.profileID)
			if s.deps.ProfileName != nil {
				name = s.deps.ProfileName(d.profileID)
			}
			ui.Text(c, name).SingleLine().MinWidth(0).Shrink(1).TextColor(p.Muted).Tooltip(name)
		})
		hint := locales.Text("JSON 中 bytes 使用 Base64，整数保留原生精度；请求使用绝对键")
		if isAuth {
			hint = locales.Text("用户与角色属于集群；每次 RPC 由服务端判断权限")
		}
		if isBrowser {
			hint = locales.Text("集群租约观察；TTL 为逐项读回值，翻页会重新读取列表")
		}
		ui.Text(c, hint).Padding(2, 8).FontSize(theme.Font(c, 11)).TextColor(p.Muted).SingleLine().Tooltip(hint)
		if isEvent && !d.eventsAvailable {
			ui.Text(c, locales.Text("此连接不支持事件请求")).Padding(3, 8).TextColor(p.Warning)
		}
		if isBrowser && !d.LeaseBrowsingAvailable() {
			ui.Text(c, locales.Text("此连接不支持租约浏览")).Padding(3, 8).TextColor(p.Warning)
		}
		if isRenewal {
			ui.Text(c, locales.Text("停止续租后，原租约及关联键可能自然过期")).Padding(2, 8).FontSize(theme.Font(c, 11)).TextColor(p.Warning).SingleLine().Tooltip(locales.Text("停止续租后，原租约及关联键可能自然过期"))
			if !d.renewalAvailable {
				ui.Text(c, locales.Text("此连接不支持持续续租")).Padding(3, 8).TextColor(p.Warning)
			}
		}
		if d.method == HistoryMethod {
			ui.Text(c, locales.Text("历史窗口在本次读取开始时固定；后续写入不会移动终点")).Padding(2, 8).FontSize(theme.Font(c, 11)).TextColor(p.Muted).SingleLine()
		}
		if d.error != "" {
			ui.Text(c, clipped(d.error)).Padding(3, 8).TextColor(p.Error).SingleLine().Tooltip(clipped(d.error))
		}
		if d.Busy() {
			status := locales.Text("等待原执行器确认；不会自动重试或重放")
			if d.eventJob != nil && d.eventJob.kind == "watch" && !d.CancelRequested() && eventWatchCreated(d.eventReceipt) {
				status = locales.Text("正在观察原键事件")
			}
			if d.renewalJob != nil && d.renewalJob.kind == "renew" && !d.CancelRequested() && renewalAcknowledged(d.renewalReceipt) > 0 {
				status = locales.Text("正在持续续租原租约")
			}
			ui.Text(c, status).Padding(3, 8).TextColor(p.Warning)
			c.AnimationFrame()
		}
		if d.hasUnknownWrites() {
			ui.Text(c, locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执")).Padding(3, 8).TextColor(p.Warning).SingleLine().Tooltip(locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执"))
		}
		if d.NeedsRecovery() {
			ui.Toolbar(c, func() {
				if s.deps.ConnectProfile != nil && ui.Button(c, locales.Text("恢复原连接")).Disabled(d.Busy()).Clicked() {
					s.deps.ConnectProfile(c.Services(), d.profileID)
				}
				if ui.Button(c, locales.Text("建立新会话（不执行现有请求）")).Disabled(d.Busy()).Clicked() {
					s.Recover(d)
				}
			}).Label(locales.Text("KV 显式恢复")).FillWidth().Padding(3, 8)
		}
		split := &s.SplitPercent
		if isAuth {
			split = &s.AuthSplitPercent
		}
		if isBrowser {
			split = &s.LeaseBrowserSplitPercent
		}
		ui.SplitVerticalPercent(c, split, func() {
			if isAuth {
				s.authManagementView(c, d)
				return
			}
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				d.Editor.Label = locales.Text("KV JSON 编辑器")
				d.Editor.ReadOnly = d.Busy()
				e := editor.View(c, d.Editor).Fill()
				if s.deps.EditFocus != nil {
					e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
				}
				if d.focus {
					e.Focus()
					d.focus = false
				}
				handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
				e.Bind(handle)
				s.registerEditor(d, e.Focused(), handle)
				c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.Run(d) })
			})
		}, func() { s.receiptView(c, d) }).Grow(1).MinHeight(0).FillWidth()
	})
}
func (s *State) receiptView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	ui.Column(c).Fill().Padding(5, 8).Gap(4).Children(func() {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("最近回执")).Clicked() {
				d.selectedBrowser = false
				d.selectedUnknown = false
				d.selectedEvents = false
				d.selectedRenewal = false
			}
			if d.unknown != nil && ui.Button(c, locales.Text("原未确认回执")).Clicked() {
				d.selectedBrowser = false
				d.selectedUnknown = true
				d.selectedEvents = false
				d.selectedRenewal = false
			}
			if (d.eventsAvailable || d.eventReceipt != nil) && ui.Button(c, locales.Text("事件回执")).Clicked() {
				d.selectedBrowser = false
				d.selectedEvents = true
				d.selectedRenewal = false
				d.selectedUnknown = false
			}
			if (d.renewalAvailable || d.renewalReceipt != nil) && ui.Button(c, locales.Text("续租回执")).Clicked() {
				d.selectedBrowser = false
				d.selectedRenewal = true
				d.selectedEvents = false
				d.selectedUnknown = false
			}
			if d.renewalUnknown != nil && ui.Button(c, locales.Text("原未确认续租回执")).Clicked() {
				d.selectedBrowser = false
				d.selectedRenewal = true
				d.selectedEvents = false
				d.selectedUnknown = false
			}
			if (d.LeaseBrowsingAvailable() || d.browserLast != nil) && ui.Button(c, locales.Text("租约浏览")).Clicked() {
				d.selectedBrowser = true
				d.selectedRenewal = false
				d.selectedEvents = false
				d.selectedUnknown = false
			}
		}).Label(locales.Text("KV 回执操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if d.selectedBrowser {
			s.leaseBrowserReceiptView(c, d)
			return
		}
		ui.Toolbar(c, func() {
			if d.selectedRenewal {
				if ui.Button(c, locales.Text("复制完整续租记录")).Disabled(d.renewalReceipt == nil || d.FileBusy()).Clicked() {
					s.CopyRenewalRecord(c.Services(), d)
				}
				if ui.Button(c, locales.Text("导出续租记录")).Disabled(d.renewalReceipt == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
					s.ExportRenewalRecord(d)
				}
				return
			}
			if d.selectedEvents {
				if ui.Button(c, locales.Text("复制完整事件记录")).Disabled(d.eventReceipt == nil || d.FileBusy()).Clicked() {
					s.CopyEventRecord(c.Services(), d)
				}
				if ui.Button(c, locales.Text("导出事件记录")).Disabled(d.eventReceipt == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
					s.ExportEventRecord(d)
				}
				return
			}
			r := s.selectedReceipt(d)
			if ui.Button(c, locales.Text("复制完整 JSON")).Disabled(r == nil || r.Result.JSON == "" && r.AuthManagement == nil).Clicked() {
				if r.AuthManagement != nil {
					raw, err := encodeOperationReceipt(*r)
					if err == nil {
						c.WriteClipboard(string(raw))
					}
				} else {
					c.WriteClipboard(r.Result.JSON)
				}
			}
			if ui.Button(c, locales.Text("导出操作记录")).Disabled(r == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
				s.ExportOperationRecord(d)
			}
			ui.Button(c, locales.Text("导出原生回执")).Disabled(r == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Menu(func(m *ui.Menu) {
				if m.Item("protobuf (.pb)").Disabled(r == nil || len(r.Result.Native) == 0).Chosen() {
					s.ExportReceipt(d, true)
				}
				if m.Item("JSON (.json)").Disabled(r == nil || r.Result.JSON == "").Chosen() {
					s.ExportReceipt(d, false)
				}
			})
		}).Label(locales.Text("KV 回执操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if d.selectedRenewal {
			s.renewalReceiptView(c, d)
			return
		}
		if d.selectedEvents {
			s.eventReceiptView(c, d)
			return
		}
		r := s.selectedReceipt(d)
		if r == nil {
			ui.Text(c, locales.Text("运行请求后显示原生回执")).TextColor(p.Muted)
			return
		}
		if r.LeaseManagement != nil {
			ui.Text(c, fmt.Sprintf("ID %d · TTL %d", r.LeaseManagement.RequestedID, r.LeaseManagement.RequestedTTL)).SingleLine()
		}
		if id, valid := acknowledgedGrantID(r); valid {
			ui.Text(c, fmt.Sprintf(locales.Text("服务端授予 ID：%d"), id)).Bold().SingleLine()
			if ui.Button(c, locales.Text("读取已授予租约")).Disabled(d.Busy() || d.needsRecovery || r != d.last || r.Error != "" || !sameEventScope(r.Scope, d.scope)).Clicked() {
				s.ReadGrantedLease(d)
			}
		}
		ui.Text(c, ReceiptStatus(r.Result)).Bold().TextColor(p.Text)
		h := r.Result.Header
		ui.Text(c, fmt.Sprintf("%s · revision %d · cluster %d · member %d", r.Result.Code, h.Revision, h.ClusterID, h.MemberID)).SingleLine().Tooltip(fmt.Sprintf("%s\n%s\nPrefix (Base64): %s", r.Result.Request.Method, r.Scope.Version, base64.StdEncoding.EncodeToString(r.Scope.Prefix)))
		if r.Result.JSONCaptureError != "" {
			ui.Text(c, r.Result.JSONCaptureError).TextColor(p.Warning).SingleLine().Tooltip(r.Result.JSONCaptureError)
		}
		if r.Result.ProjectionError != "" {
			ui.Text(c, r.Result.ProjectionError).TextColor(p.Warning).SingleLine().Tooltip(r.Result.ProjectionError)
		}
		if ui.Button(c, locales.Text("复制原请求")).Clicked() {
			c.WriteClipboard(r.Result.Request.Method + "\n" + r.Result.Request.JSON)
		}
		text := r.Result.JSON
		if r.AuthManagement != nil {
			raw, _ := json.MarshalIndent(authRecord(r.AuthManagement), "", "  ")
			text = string(raw)
			ui.Text(c, fmt.Sprintf(locales.Text("已返回 %d / %d 个原步骤回执"), len(r.AuthManagement.Steps), len(r.AuthManagement.Requests))).SingleLine()
		}
		if text == "" {
			text = r.Error
		}
		if text == "" {
			text = ReceiptStatus(r.Result)
		}
		if len(text) > previewLimit {
			ui.Text(c, locales.Text("预览最多 64 KiB；复制与导出保留完整内容")).FontSize(theme.Font(c, 11)).TextColor(p.Muted)
		}
		preview := clipped(text)
		area := ui.TextArea(c, &preview).Label(locales.Text("KV 原生回执")).ReadOnly(true).Fill().MinHeight(0)
		theme.BindNativeEdit(c, area, fmt.Sprintf("kv/receipt/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
	})
}
func (s *State) Modals(c *ui.Context) {
	s.renewalModals(c)
	s.leaseManagementModals(c)
	s.authManagementModals(c)
	for _, d := range s.documents {
		if !s.active(d) || d.confirmation == nil || s.leave != nil {
			continue
		}
		confirmation := d.confirmation
		theme.Modal(c.Key(fmt.Sprintf("kv-confirm-%d", d.id)), &confirmation.open, func() {
			if confirmation.auth != nil {
				s.authConfirmationView(c, d, confirmation)
				return
			}
			if confirmation.management != nil {
				s.leaseManagementConfirmationView(c, d, confirmation)
				return
			}
			ui.Column(c).Width(560).Gap(8).Children(func() {
				ui.Text(c, locales.Text("确认 KV 写入请求")).Bold()
				ui.Text(c, confirmation.plan.Snapshot().Method).SingleLine().Tooltip(confirmation.plan.Snapshot().Method)
				ui.Text(c, locales.Text("仅派发一次；取消不代表回滚，丢失回执后必须显式核对")).TextColor(theme.Colors(c).Warning)
				preview := clipped(confirmation.plan.Snapshot().JSON)
				ui.TextArea(c, &preview).Label(locales.Text("KV 写入预览")).ReadOnly(true).Height(160).FillWidth()
				if ui.Button(c, locales.Text("复制完整请求")).Clicked() {
					r := confirmation.plan.Snapshot()
					c.WriteClipboard(r.Method + "\n" + r.JSON)
				}
				ui.Checkbox(c, &confirmation.checked, locales.Text("我确认执行此原生写入请求"))
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						s.CancelConfirmation(d)
					}
					if ui.Button(c, locales.Text("确认执行 KV 写入")).Disabled(!confirmation.checked || d.Busy()).Clicked() {
						s.Confirm(d, confirmation.checked)
					}
				})
			})
		})
		if !confirmation.open && d.confirmation == confirmation {
			s.CancelConfirmation(d)
		}
	}
	if s.leave != nil {
		decision := s.leave
		d := decision.doc
		open := true
		theme.Modal(c.Key("kv-leave"), &open, func() {
			ui.Column(c).Width(500).Gap(8).Children(func() {
				ui.Text(c, locales.Text("离开 KV 请求页签？")).Bold()
				name := d.Name()
				if s.deps.ProfileName != nil {
					name = s.deps.ProfileName(d.profileID) + " · " + name
				}
				ui.Text(c, name).SingleLine()
				if d.Busy() || d.FileBusy() {
					ui.Text(c, locales.Text("等待原执行器确认；不会自动重试或重放"))
					c.AnimationFrame()
					if ui.Button(c, locales.Text("取消操作并等待")).Clicked() {
						s.RequestCancel(d)
						s.CancelFile(d)
					}
				} else if d.hasUnknownWrites() {
					ui.Text(c, locales.Text("关闭只放弃本地草稿与回执，不确认或回滚原写入")).TextColor(theme.Colors(c).Warning)
				} else {
					ui.Text(c, locales.Text("未保存的请求文本将被放弃"))
				}
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						s.DecideLeave(c.Services(), false)
					}
					if ui.Button(c, locales.Text("放弃本地内容并继续")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
						s.DecideLeave(c.Services(), true)
					}
				})
			})
		})
		if !open && s.leave == decision {
			s.DecideLeave(c.Services(), false)
		}
	}
}
func (s *State) registerEditor(d *Document, focused bool, handle *ui.Handle) {
	if !focused || s.deps.EditTarget == nil {
		return
	}
	commands := map[string]editor.Command{"undo": editor.Undo, "redo": editor.Redo, "cut": editor.Cut, "copy": editor.Copy, "paste": editor.Paste, "select_all": editor.SelectAll, "find": editor.Find, "replace": editor.FindReplace}
	s.deps.EditTarget(theme.EditTarget{ID: fmt.Sprintf("kv/editor/%d", d.id), Handle: handle, Valid: func() bool { return s.active(d) && d.Editor != nil }, Enabled: func(command string) bool {
		if command == "copy" || command == "cut" {
			return d.Editor.SelectedText() != "" && (command == "copy" || !d.Editor.ReadOnly)
		}
		if command == "undo" {
			return !d.Editor.ReadOnly && d.Editor.Buffer.CanUndo()
		}
		if command == "redo" {
			return !d.Editor.ReadOnly && d.Editor.Buffer.CanRedo()
		}
		_, ok := commands[command]
		return ok && (!d.Editor.ReadOnly || command == "select_all" || command == "find")
	}, Run: func(_ ui.Services, command string) bool { return d.Editor.Run(commands[command]) }})
}
