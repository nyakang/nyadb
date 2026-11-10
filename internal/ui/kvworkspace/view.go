package kvworkspace

import (
	"encoding/base64"
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
	ui.Column(c).Fill().Background(p.Panel).Children(func() {
		ui.Row(c).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Background(p.Header).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("运行请求")).Label(locales.Text("运行 KV 请求")).Disabled(d.Busy() || d.needsRecovery || d.lease == nil).Clicked() {
					s.Run(d)
				}
				if ui.Button(c, locales.Text("取消")).Disabled(!d.Busy() || d.CancelRequested()).Clicked() {
					s.RequestCancel(d)
				}
				if ui.Button(c, locales.Text("预连接")).Disabled(d.Busy() || d.needsRecovery || d.lease == nil).Clicked() {
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
			for _, m := range methods {
				options = append(options, m.Name)
				if m.Path == d.method {
					choice = m.Name
				}
			}
			if ui.Select(c, &choice, options).Label(locales.Text("KV 方法")).Width(165).Disabled(d.Busy()).Changed() {
				for _, m := range methods {
					if m.Name == choice {
						s.SetMethod(d, m.Path)
					}
				}
			}
			name := fmt.Sprintf(locales.Text("连接 #%d"), d.profileID)
			if s.deps.ProfileName != nil {
				name = s.deps.ProfileName(d.profileID)
			}
			ui.Text(c, name).SingleLine().MinWidth(0).Shrink(1).TextColor(p.Muted).Tooltip(name)
		})
		ui.Text(c, locales.Text("JSON 中 bytes 使用 Base64，整数保留原生精度；请求使用绝对键")).Padding(2, 8).FontSize(theme.Font(c, 11)).TextColor(p.Muted).SingleLine().Tooltip(d.method)
		if d.error != "" {
			ui.Text(c, clipped(d.error)).Padding(3, 8).TextColor(p.Error).SingleLine().Tooltip(clipped(d.error))
		}
		if d.Busy() {
			ui.Text(c, locales.Text("等待原执行器确认；不会自动重试或重放")).Padding(3, 8).TextColor(p.Warning)
			c.AnimationFrame()
		}
		if d.unknown != nil {
			ui.Text(c, locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执")).Padding(3, 8).TextColor(p.Warning).SingleLine().Tooltip(locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执"))
		}
		if d.needsRecovery {
			ui.Toolbar(c, func() {
				if s.deps.ConnectProfile != nil && ui.Button(c, locales.Text("恢复原连接")).Disabled(d.Busy()).Clicked() {
					s.deps.ConnectProfile(c.Services(), d.profileID)
				}
				if ui.Button(c, locales.Text("建立新会话（不执行现有请求）")).Disabled(d.Busy()).Clicked() {
					s.Recover(d)
				}
			}).Label(locales.Text("KV 显式恢复")).FillWidth().Padding(3, 8)
		}
		ui.SplitVerticalPercent(c, &s.SplitPercent, func() {
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
				d.selectedUnknown = false
			}
			if d.unknown != nil && ui.Button(c, locales.Text("原未确认回执")).Clicked() {
				d.selectedUnknown = true
			}
			r := s.selectedReceipt(d)
			if ui.Button(c, locales.Text("复制完整 JSON")).Disabled(r == nil || r.Result.JSON == "").Clicked() {
				c.WriteClipboard(r.Result.JSON)
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
		r := s.selectedReceipt(d)
		if r == nil {
			ui.Text(c, locales.Text("运行请求后显示原生回执")).TextColor(p.Muted)
			return
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
	for _, d := range s.documents {
		if !s.active(d) || d.confirmation == nil || s.leave != nil {
			continue
		}
		confirmation := d.confirmation
		theme.Modal(c.Key(fmt.Sprintf("kv-confirm-%d", d.id)), &confirmation.open, func() {
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
				} else if d.unknown != nil {
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
