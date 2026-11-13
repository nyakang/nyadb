package kvworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) httpSessionDestroyConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	x := p.destroy
	colors := theme.Colors(c)
	ui.Column(c).Width(540).Height(430).Gap(5).Children(func() {
		ui.Text(c, locales.Text("确认销毁原 Session")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), x.preview.Scope.Namespace.ProfileID, x.preview.Scope.Generation)).SingleLine()
		ui.Text(c, "Session UUID: "+x.preview.Request.ID).SingleLine().Tooltip(x.preview.Request.ID)
		ui.Text(c, fmt.Sprintf(locales.Text("Behavior %s · 观察到 %d 个持有键"), x.behavior, len(x.preview.Keys.Matches))).SingleLine()
		for _, message := range []string{"全根目录观察；连接前缀不限定持有键范围", "release 释放锁；delete 删除此 Session 持有的键", "确认前重新检查 UUID、Behavior 和持有键版本；读取与销毁之间仍可能变化"} {
			ui.Text(c, locales.Text(message)).Wrap().FillWidth().Tooltip(locales.Text(message)).TextColor(colors.Warning)
		}
		indices := x.preview.Keys.Matches
		pages := max(1, (len(indices)+httpSessionPageSize-1)/httpSessionPageSize)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("上一页")).Disabled(x.page == 0).Clicked() {
				x.page--
				p.keyColumns.ScrollTo(0, ui.Start)
			}
			if ui.Button(c, locales.Text("下一页")).Disabled(x.page+1 >= pages).Clicked() {
				x.page++
				p.keyColumns.ScrollTo(0, ui.Start)
			}
			ui.Text(c, fmt.Sprintf("%d/%d", x.page+1, pages)).SingleLine()
			if ui.Button(c, locales.Text("复制完整 Session 销毁预览")).Clicked() {
				raw, err := encodeHTTPSessionDestroyReceipt(Receipt{Scope: x.preview.Scope.Clone(), Authored: p.authored, Result: contract.KVResult{Request: p.authored, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: x.recordID, Preview: x.preview}}})
				if err == nil {
					c.WriteClipboard(string(raw))
				}
			}
		}).FillWidth().MinWidth(0).Gap(5)
		start := min(x.page*httpSessionPageSize, len(indices))
		page := indices[start:min(start+httpSessionPageSize, len(indices))]
		theme.Table(c, &p.keyColumns, []ui.TableColumn{{Title: "Key", Width: 360}, {Title: "ModifyIndex", Width: 160}}, len(page), func(row, column int) {
			key := x.preview.Keys.Root.HTTP.Entries[page[row]]
			text := key.Key
			if column == 1 {
				text = strconv.FormatUint(key.ModifyIndex, 10)
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Label(locales.Text("原 Session 持有键与版本")).Fill().MinHeight(0)
		ui.Checkbox(c, &p.checked, locales.Text("我确认销毁此原 Session"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认销毁 Session")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}

func (s *State) httpSessionDestroyReceiptView(c *ui.Context, d *Document, r *Receipt) {
	e := r.Result.HTTPSessionDestroy
	ui.Text(c, locales.Text("Session 销毁原始记录")).Bold()
	ui.Text(c, "Session UUID: "+e.Preview.Request.ID).SingleLine().Tooltip(e.Preview.Request.ID)
	ui.Text(c, ReceiptStatus(r.Result)).SingleLine()
	ui.Text(c, locales.Text("取消不代表回滚；未知写入回执不会被后来读取清除")).Wrap().FillWidth().Tooltip(locales.Text("取消不代表回滚；未知写入回执不会被后来读取清除")).TextColor(theme.Colors(c).Warning)
	options := []string{locales.Text("预览 Session 原始响应"), locales.Text("预览全根目录原始响应"), locales.Text("重新检查 Session 原始响应"), locales.Text("重新检查全根目录原始响应"), locales.Text("原销毁 PUT 回执")}
	choice := options[min(d.httpSessionDestroyStep, len(options)-1)]
	if ui.Select(c, &choice, options).Label(locales.Text("选择原始步骤回执")).FillWidth().Changed() {
		for i, v := range options {
			if v == choice {
				d.httpSessionDestroyStep = i
			}
		}
	}
	steps := []*contract.KVHTTPResult{e.Preview.Session.HTTP, e.Preview.Keys.Root.HTTP, e.Recheck.Session.HTTP, e.Recheck.Keys.Root.HTTP, r.Result.HTTP}
	h := steps[min(d.httpSessionDestroyStep, len(steps)-1)]
	text := r.Error
	if h != nil {
		ui.Text(c, fmt.Sprintf("%s %s · %d", h.Request.Method, h.Request.Path, h.Status)).SingleLine()
		text = httpReceiptPreview(h)
	} else {
		ui.Text(c, locales.Text("此步骤没有原生完成回执")).SingleLine().TextColor(theme.Colors(c).Muted)
	}
	area := ui.TextArea(c, &text).Label(locales.Text("KV 原生回执")).ReadOnly(true).Fill().MinHeight(0)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/destroy-receipt/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}
