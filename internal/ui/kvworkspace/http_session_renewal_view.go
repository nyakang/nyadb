package kvworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Reformat known application messages at render time. The stored error and
// original HTTP evidence keep the language and bytes captured by the operation.
func httpSessionRenewalDisplayError(value string) string {
	for _, source := range []string{
		"KV 原连接不可用",
		"原续租未确认；此文档禁止继续写入",
		"请先复制或导出，再明确清除原续租记录",
		"KV 请求已更改，请重新预览",
	} {
		for _, language := range []string{"zh-Hans", "en"} {
			if value == locales.TextIn(language, source) {
				return locales.Text(source)
			}
		}
	}
	for _, language := range []string{"zh-Hans", "en"} {
		prefix := locales.TextIn(language, "KV 请求无效") + ": "
		if strings.HasPrefix(value, prefix) {
			return locales.Text("KV 请求无效") + ": " + strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func (s *State) httpSessionRenewalRequestView(c *ui.Context, d *Document) {
	w := d.httpSessionRenewalState()
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Gap(3).Padding(3, 8).Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("续租表单")).Clicked() {
				w.jsonMode = false
			}
			if ui.Button(c, locales.Text("续租 JSON")).Clicked() {
				w.jsonMode = true
				d.focus = true
			}
		})
		if w.jsonMode {
			d.Editor.Label = locales.Text("KV JSON 编辑器")
			d.Editor.ReadOnly = d.Busy() || d.FileBusy()
			e := editor.View(c, d.Editor).Fill().MinHeight(30)
			handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
			e.Bind(handle)
			if s.deps.EditFocus != nil {
				e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
			}
			s.registerEditor(d, e.Focused(), handle)
			if d.focus {
				e.Focus()
				d.focus = false
			}
		} else {
			ui.Scroll(c).Fill().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Gap(3).Children(func() {
					input, err := httpSessionRenewalInput(d.Request().JSON)
					if err != nil {
						ui.Text(c, locales.Text("请求 JSON 无效；请切换 JSON 编辑或使用请求模板")).Wrap().FillWidth().TextColor(p.Warning)
						return
					}
					changed := false
					field := func(label string, value *string) {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, label).Width(150)
							entry := ui.TextInput(c, value).Label(label).Grow(1).MinWidth(0).Disabled(d.Busy() || d.FileBusy())
							theme.BindNativeEdit(c, entry, fmt.Sprintf("kv/http-session-renewal/form/%d/%s", d.id, label), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, true, false)
							if entry.Changed() {
								changed = true
							}
						})
					}
					field(locales.Text("Session UUID"), &input.SessionID)
					field(locales.Text("续租间隔"), &input.Interval)
					field(locales.Text("最多续租次数"), &input.MaxRenewals)
					field(locales.Text("记录预算（字节）"), &input.MaxBytes)
					if changed {
						s.SetHTTPSessionRenewalInput(d, input)
					}
					ui.Text(c, locales.Text("间隔 100ms 至 1h；次数 1 至 256；预算最多 256 MiB；原 TTL 必须大于间隔")).SingleLine().Tooltip(locales.Text("间隔 100ms 至 1h；次数 1 至 256；预算最多 256 MiB；原 TTL 必须大于间隔")).TextColor(p.Muted)
					if w.lease != nil {
						lim := w.lease.Limits()
						ui.Text(c, fmt.Sprintf(locales.Text("原 timeout %s · connect %s · 终末槽 %d 字节"), lim.RequestTimeout.String(), lim.ConnectTimeout.String(), lim.TerminalBytes)).SingleLine().TextColor(p.Muted)
					}
				})
			})
		}
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareHTTPSessionRenewal(d) })
	})
}

func (s *State) httpSessionRenewalReceiptView(c *ui.Context, d *Document) {
	w := d.httpSessionRenewal
	p := theme.Colors(c)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制完整续租记录")).Disabled(!d.httpSessionRenewalContent() || d.FileBusy()).Clicked() {
			s.CopyHTTPSessionRenewalRecord(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出续租记录")).Disabled(!d.httpSessionRenewalContent() || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
			s.ExportHTTPSessionRenewalRecord(d)
		}
	}).Label(locales.Text("HTTP Session 记录操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
	if w == nil || !d.httpSessionRenewalContent() {
		ui.Text(c, locales.Text("明确确认后显示原 Session 读取与逐次续租回执")).TextColor(p.Muted)
		return
	}
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(3).Children(func() {
			ui.Text(c, fmt.Sprintf(locales.Text("回执连接 #%d · 原会话代次 %d"), w.evidenceScope.Namespace.ProfileID, w.evidenceScope.Generation)).SingleLine().TextColor(p.Muted)
			state := locales.Text("正在续租原 HTTP Session")
			if w.status.Ended {
				state = locales.Text("原 HTTP Session 续租已停止")
			}
			if w.unknown {
				state = locales.Text("原续租未确认；此文档禁止继续写入")
			}
			ui.Text(c, state).Bold().SingleLine()

			if w.status.Terminal.Reason != "" {
				ui.Text(c, w.status.Terminal.Reason).SingleLine().Tooltip(clipped(w.status.Terminal.Message)).TextColor(p.Warning)
			}
			if w.status.TerminalOverflow {
				ui.Text(c, locales.Text("已超作者预算；完整终末响应单独保留，续租已停止")).SingleLine().Tooltip(locales.Text("已超作者预算；完整终末响应单独保留，续租已停止")).TextColor(p.Warning)
			}
			if w.incomplete {
				ui.Text(c, locales.Text("原 HTTP 捕获或投影不完整；保留实际响应证据")).SingleLine().Tooltip(locales.Text("原 HTTP 捕获或投影不完整；保留实际响应证据")).TextColor(p.Warning)
			}

			if w.status.Observations > 0 {
				options := make([]string, w.status.Observations)
				for i := range options {
					options[i] = strconv.Itoa(i)
				}
				choice := strconv.Itoa(w.selected)
				if ui.Select(c, &choice, options).Label(locales.Text("原续租记录序号（0 为初始读取）")).Width(180).Changed() {
					if i, e := strconv.Atoi(choice); e == nil {
						s.SelectHTTPSessionRenewalObservation(d, i)
					}
				}
			}
			preview := w.preview
			area := ui.TextArea(c, &preview).Label(locales.Text("HTTP Session 原记录")).ReadOnly(true).FillWidth().Height(170)
			theme.BindNativeEdit(c, area, fmt.Sprintf("kv/http-session-renewal/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
			ui.Text(c, "UUID "+w.request.SessionID).SingleLine()
			ui.Text(c, fmt.Sprintf(locales.Text("原间隔 %s · 已尝试 %d / %d · 已确认成功 %d"), w.request.Interval.String(), w.status.Terminal.Attempts, w.request.MaxRenewals, w.status.Terminal.Acknowledged)).SingleLine()
			ui.Text(c, fmt.Sprintf(locales.Text("完整记录 %d · 已保留 %d / %d 字节"), w.status.Observations, w.status.RetainedBytes, w.request.MaxBytes)).SingleLine()
			ui.Text(c, locales.Text("停止不销毁 Session，也不表示已经过期；恢复不会重新续租")).SingleLine().Tooltip(locales.Text("停止不销毁 Session，也不表示已经过期；恢复不会重新续租")).TextColor(p.Muted)
		})
	})
}
func (s *State) httpSessionRenewalModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpSessionRenewal == nil || d.httpSessionRenewal.confirmation == nil || s.leave != nil {
			continue
		}
		confirm := d.httpSessionRenewal.confirmation
		theme.Modal(c.Key(fmt.Sprintf("kv-http-session-renewal-confirm-%d", d.id)), &confirm.open, func() {
			ui.Column(c).Width(570).Gap(5).Children(func() {
				r := confirm.request
				ui.Text(c, locales.Text("确认 HTTP Session 续租")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), confirm.scope.Namespace.ProfileID, confirm.scope.Generation)).SingleLine()
				endpoint := fmt.Sprintf(locales.Text("原端点 %s · DC %s"), confirm.scope.HTTP.Endpoint, confirm.scope.HTTP.Datacenter)
				ui.Text(c, endpoint).SingleLine().Tooltip(endpoint)
				ui.Text(c, "UUID "+r.SessionID).SingleLine().Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("间隔 %s · 最多 %d 次 PUT · 记录预算 %d 字节"), r.Interval.String(), r.MaxRenewals, r.MaxBytes)).SingleLine()
				ui.Text(c, locales.Text("先读取此 Session 的原 TTL，再开始有限续租；每次 PUT 都是独立写入。")).Wrap().FillWidth()
				ui.Text(c, locales.Text("停止不销毁 Session，也不表示已经过期；恢复不会重新续租")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
				preview := clipped(confirm.authored.JSON)
				ui.TextArea(c, &preview).Label(locales.Text("原 HTTP Session 续租请求")).ReadOnly(true).Height(70).FillWidth()
				ui.Checkbox(c, &confirm.checked, locales.Text("我授权按以上 UUID、间隔、次数和预算续租"))
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("复制完整请求")).Clicked() {
						c.WriteClipboard(confirm.authored.Method + "\n" + confirm.authored.JSON)
					}
					if ui.Button(c, locales.Text("取消")).Clicked() {
						s.CancelConfirmation(d)
					}
					if ui.Button(c, locales.Text("确认并开始续租")).Disabled(!confirm.checked || d.Busy() || d.FileBusy() || d.hasUnknownWrites()).Clicked() {
						s.ConfirmHTTPSessionRenewal(d, confirm.checked)
					}
				})
			})
		})
		if !confirm.open && d.httpSessionRenewal != nil && d.httpSessionRenewal.confirmation == confirm {
			s.CancelConfirmation(d)
		}
	}
}
