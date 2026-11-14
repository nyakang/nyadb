package kvworkspace

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) httpACLMutationModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpACLMutation == nil || d.httpACLMutation.form == nil || !d.httpACLMutation.form.open || s.leave != nil {
			continue
		}
		x := d.httpACLMutation
		f := x.form
		theme.Modal(c.Key(fmt.Sprintf("kv-acl-write-%d", d.id)), &f.open, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Text(c, locales.Text("ACL 原生写入")).Bold()
				if (!f.receipt || x.last == nil) && !f.impactOpen {
					ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), d.profileID, d.scope.Generation)).SingleLine()
				}
				if x.transfer != nil {
					s.httpACLMutationTransferView(c, d)
					return
				}
				if d.Busy() {
					s.httpACLMutationReceiptView(c, d)
					return
				}
				if f.impactOpen {
					s.httpACLMutationImpactView(c, d)
					return
				}
				if p := x.confirmation; p != nil {
					s.httpACLMutationConfirmView(c, d)
					return
				}
				if f.receipt {
					s.httpACLMutationReceiptView(c, d)
					return
				}
				s.httpACLMutationInputView(c, d)
			})
		})
		if !f.open {
			s.HideHTTPACLMutationReceipt(d)
			d.clearHTTPACLMutationConfirmation()
		}
	}
}
func (s *State) httpACLMutationInputView(c *ui.Context, d *Document) {
	x := d.httpACLMutation
	f := x.form
	disabled := d.Busy() || d.FileBusy()
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("打开 ACL 写入输入")).Disabled(disabled || s.deps.Files.OpenPath == nil || s.deps.Files.OpenReader == nil).Clicked() {
			s.openHTTPACLMutationInput(d)
		}
		if ui.Button(c, locales.Text("保存 ACL 写入输入…")).Disabled(disabled || s.deps.Files.WriteStream == nil || f.path == "" && s.deps.Files.SavePath == nil).Clicked() {
			s.prepareHTTPACLMutationTransfer(d, "save-acl-write-input")
		}
		if ui.Button(c, locales.Text("另存 ACL 写入输入…")).Disabled(disabled || s.deps.Files.WriteStream == nil || s.deps.Files.SavePath == nil).Clicked() {
			s.prepareHTTPACLMutationTransfer(d, "save-as-acl-write-input")
		}
	}).FillWidth().MinWidth(0).Gap(5)
	input := f.input
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, locales.Text("ACL 资源类型")).SingleLine()
					ui.Select(c, &input.Resource, []string{"token", "policy", "role", "auth-method", "binding-rule"}).Label(locales.Text("ACL 资源类型")).FillWidth().Disabled(disabled)
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, locales.Text("ACL 写入操作")).SingleLine()
					ui.Select(c, &input.Operation, []string{"create", "update", "delete", "clone"}).Label(locales.Text("ACL 写入操作")).FillWidth().Disabled(disabled)
				})
			})
			ui.Text(c, locales.Text("ACL 原标识或名称")).SingleLine()
			ui.TextInput(c, &input.Identity).Label(locales.Text("ACL 原标识或名称")).FillWidth().Disabled(disabled || input.Operation == "create")
			ui.Text(c, locales.Text("Token 使用 AccessorID；AuthMethod 使用原名称。克隆仅支持 Token。")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
			if input.Operation == "create" || input.Operation == "update" {
				ui.Text(c, locales.Text("ACL 原始定义 JSON（可能含秘密）")).SingleLine()
				ui.TextArea(c, &input.Definition).Label(locales.Text("ACL 原始定义 JSON（可能含秘密）")).Height(130).FillWidth().Disabled(disabled)
			} else if input.Operation == "clone" {
				ui.Checkbox(c, &input.IncludeDescription, locales.Text("提供克隆 Description")).Disabled(disabled)
				ui.TextInput(c, &input.Description).Label("Description").FillWidth().Disabled(disabled || !input.IncludeDescription)
				ui.Text(c, locales.Text("省略或留空 Description 将继承原 Token 描述。")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
			}
		})
	})
	s.SetHTTPACLMutationForm(d, input)
	if d.error != "" {
		message := d.error
		if message == httpACLMutationError {
			message = locales.Text("ACL 写入失败；原草稿与私密回执保留")
		}
		ui.Text(c, message).SingleLine().Tooltip(message).TextColor(theme.Colors(c).Warning)
	}
	if x.observation != nil && ui.Button(c, locales.Text("查看 ACL 影响记录")).Disabled(disabled).Clicked() {
		f.impactOpen = true
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("检查并确认 ACL 写入")).Disabled(disabled || d.NeedsRecovery() || d.hasUnknownWrites()).Clicked() {
			s.PrepareHTTPACLMutationForm(d)
		}
		if ui.Button(c, locales.Text("复制 ACL 写入输入…")).Disabled(disabled).Clicked() {
			s.prepareHTTPACLMutationTransfer(d, "copy-acl-write-input")
		}
		if ui.Button(c, locales.Text("返回请求文本")).Clicked() {
			d.clearHTTPACLMutationConfirmation()
			f.open = false
			s.HideHTTPACLMutationReceipt(d)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	if d.FileBusy() && ui.Button(c, locales.Text("取消文件操作")).Clicked() {
		s.CancelFile(d)
	}
}
func (s *State) httpACLMutationConfirmView(c *ui.Context, d *Document) {
	x := d.httpACLMutation
	f := x.form
	p := x.confirmation
	ui.Text(c, locales.Text("确认原连接 ACL 写入")).Bold()
	ui.Text(c, p.request.Resource+" · "+p.request.Operation+" · "+p.request.Identity).SingleLine()
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, locales.Text("此操作会修改原连接的访问控制。取消或断开后，将等待原执行器确认，不会自动重放。")).Wrap().FillWidth()
			httpACLMutationImpactSummary(c, p.impact)
			if ui.Button(c, locales.Text("查看 ACL 影响记录")).Clicked() {
				f.impactOpen = true
			}
			if p.request.Resource == "auth-method" && p.request.Operation == "delete" {
				ui.Text(c, locales.Text("删除 AuthMethod 会删除关联绑定规则，并撤销该认证方式签发的令牌。")).Wrap().FillWidth()
			}
			if p.request.Resource == "policy" && p.request.Operation == "delete" {
				ui.Text(c, locales.Text("删除 Policy 后令牌与角色仍保留，关联策略链接会被移除。")).Wrap().FillWidth()
			}
			if p.request.Resource == "role" && p.request.Operation == "delete" {
				ui.Text(c, locales.Text("删除 Role 后令牌仍保留，关联角色链接会被移除。")).Wrap().FillWidth()
			}
			if p.request.Resource == "token" && p.request.Operation == "update" {
				ui.Text(c, locales.Text("更新 Token 时，省略策略或角色列表会清空相应链接；省略 SecretID 会保留原密钥。")).Wrap().FillWidth()
			}
			if ui.Button(c, locales.Text("查看待发送 ACL 内容（含秘密）")).Clicked() {
				raw, _ := json.MarshalIndent(p.request, "", "  ")
				f.preview = clipped(string(raw))
			}
			if f.preview != "" {
				ui.TextArea(c, &f.preview).Label(locales.Text("待发送 ACL 私密内容")).ReadOnly(true).Height(110).FillWidth()
			}
		})
	})
	ui.Checkbox(c, &f.checked, locales.Text("我确认执行此原连接 ACL 写入"))
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("确认执行 ACL 写入")).Disabled(!f.checked || d.Busy() || d.FileBusy()).Clicked() {
			s.ConfirmHTTPACLMutation(d, f.checked)
			f.checked = false
			f.preview = ""
			if d.job != nil {
				f.receipt = true
			}
		}
		if ui.Button(c, locales.Text("取消")).Clicked() {
			d.clearHTTPACLMutationConfirmation()
			f.preview = ""
			f.checked = false
		}
	}).FillWidth().MinWidth(0).Gap(5)
}
func (s *State) httpACLMutationReceiptView(c *ui.Context, d *Document) {
	x := d.httpACLMutation
	f := x.form
	if d.Busy() {
		ui.Text(c, locales.Text("等待原执行器确认；不会自动重试或重放")).Wrap().FillWidth()
		if ui.Button(c, locales.Text("取消操作并等待")).Clicked() {
			s.RequestCancel(d)
		}
		c.AnimationFrame()
		return
	}
	if d.error != "" {
		message := d.error
		if message == httpACLMutationError {
			message = locales.Text("ACL 写入失败；原草稿与私密回执保留")
		}
		ui.Text(c, message).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
	}
	if x.last == nil {
		ui.Text(c, locales.Text("ACL 写入失败；原草稿与私密回执保留")).Wrap().FillWidth()
	} else {
		r := x.last
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine()
		ui.Text(c, r.Request.Resource+" · "+r.Request.Operation+" · "+r.Request.Identity).SingleLine()
		if r.Receipt.HTTP != nil {
			ui.Text(c, "HTTP "+strconv.Itoa(r.Receipt.HTTP.Status)+" · "+ReceiptStatus(r.Receipt)).SingleLine()
		}
		if x.unknown {
			ui.Text(c, locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
		}
		if f.preview != "" && x.revealed == r.LocalRecordID {
			if ui.Button(c, locales.Text("隐藏 ACL 写入秘密")).Clicked() {
				s.HideHTTPACLMutationReceipt(d)
			}
			ui.TextArea(c, &f.preview).Label(locales.Text("ACL 写入原始正文")).ReadOnly(true).Fill().MinHeight(0)
		} else {
			ui.Text(c, locales.Text("秘密默认隐藏；显示、复制和导出仅使用此原回执")).Wrap().FillWidth()
			ui.Scroll(c).Fill().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					if ui.Button(c, locales.Text("显示 ACL 写入原正文（含秘密）")).Disabled(r.Receipt.HTTP == nil).Clicked() {
						if s.RevealHTTPACLMutationReceipt(d, r.LocalRecordID) {
							f.preview = clipped(string(r.Receipt.HTTP.Body))
						}
					}
					if ui.Button(c, locales.Text("查看 ACL 影响记录")).Disabled(x.observation == nil).Clicked() {
						f.impactOpen = true
					}
					if ui.Button(c, locales.Text("复制完整 ACL 写入回执…")).Disabled(d.FileBusy()).Clicked() {
						s.prepareHTTPACLMutationTransfer(d, "copy-acl-write-receipt")
					}
					if ui.Button(c, locales.Text("导出完整 ACL 写入回执…")).Disabled(d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
						s.prepareHTTPACLMutationTransfer(d, "export-acl-write-receipt")
					}
				})
			})
		}
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("返回 ACL 写入输入")).Clicked() {
			s.HideHTTPACLMutationReceipt(d)
			f.receipt = false
		}
		if d.NeedsRecovery() && ui.Button(c, locales.Text("恢复原连接")).Clicked() {
			s.Recover(d)
		}
		if ui.Button(c, locales.Text("返回请求文本")).Clicked() {
			s.HideHTTPACLMutationReceipt(d)
			f.open = false
		}
	}).FillWidth().MinWidth(0).Gap(5)
	if d.FileBusy() && ui.Button(c, locales.Text("取消文件操作")).Clicked() {
		s.CancelFile(d)
	}
}
func (s *State) httpACLMutationTransferView(c *ui.Context, d *Document) {
	p := d.httpACLMutation.transfer
	ui.Text(c, locales.Text("传出 ACL 私密内容")).Bold()
	ui.Text(c, locales.Text("此内容可能包含 Token SecretID、AuthMethod 配置或原生错误正文。请选择可信的剪贴板或文件位置。")).Wrap().FillWidth()
	ui.Checkbox(c, &p.checked, locales.Text("我确认传出此原输入或回执中的秘密"))
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("确认传出 ACL 写入内容")).Disabled(!p.checked).Clicked() {
			s.confirmHTTPACLMutationTransfer(c.Services(), d, p.checked)
		}
		if ui.Button(c, locales.Text("取消")).Clicked() {
			d.httpACLMutation.transfer = nil
		}
	}).FillWidth().MinWidth(0).Gap(5)
}
