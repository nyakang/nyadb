package kvworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) httpWatchRequestView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Gap(3).Padding(3, 8).Children(func() {
		if r, err := ParseHTTPWatchRequest(d.Request().JSON); err == nil {
			mode := locales.Text("精确键")
			if r.Prefix {
				mode = locales.Text("前缀")
			}
			ui.Text(c, fmt.Sprintf(locales.Text("原目标（%s）：%s"), mode, strconv.Quote(r.Target))).Label(locales.Text("Watch 原目标")).SingleLine().Tooltip(clipped(strconv.Quote(r.Target)))
			ui.Text(c, fmt.Sprintf(locales.Text("实际读取前缀：%s"), strconv.Quote(r.ObservationPrefix))).Label(locales.Text("Watch 实际范围")).SingleLine().Tooltip(clipped(strconv.Quote(r.ObservationPrefix)))
			ui.Text(c, fmt.Sprintf(locales.Text("起始 index %s · wait %s · 最多 %d 笔 · %d 字节"), strconv.FormatUint(r.StartIndex, 10), r.Wait.String(), r.MaxObservations, r.MaxBytes)).SingleLine().TextColor(p.Muted)
		}
		if d.httpWatch != nil && d.httpWatch.lease != nil {
			l := d.httpWatch.lease.Limits()
			ui.Text(c, fmt.Sprintf(locales.Text("原 timeout %s · connect %s · 终末槽 %d 字节"), l.RequestTimeout.String(), l.ConnectTimeout.String(), l.TerminalBytes)).SingleLine().TextColor(p.Muted)
		}
		d.Editor.Label = locales.Text("KV JSON 编辑器")
		d.Editor.ReadOnly = d.Busy()
		e := editor.View(c, d.Editor).Fill().MinHeight(30)
		handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
		e.Bind(handle)
		if s.deps.EditFocus != nil {
			e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
		}
		s.registerEditor(d, e.Focused(), handle)
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.StartHTTPWatch(d) })
	})
}

func (s *State) httpWatchReceiptView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	w := d.httpWatch
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制完整 Watch 记录")).Disabled(w == nil || w.run == nil || d.FileBusy()).Clicked() {
			s.CopyHTTPWatchRecord(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出 Watch 记录")).Disabled(w == nil || w.run == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
			s.ExportHTTPWatchRecord(d)
		}
	}).Label(locales.Text("Watch 记录操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
	if w == nil || w.run == nil {
		ui.Text(c, locales.Text("明确启动后显示原 HTTP 观察")).TextColor(p.Muted)
		return
	}
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(3).Children(func() {
			ui.Text(c, fmt.Sprintf(locales.Text("回执连接 #%d · 原会话代次 %d"), w.evidenceScope.Namespace.ProfileID, w.evidenceScope.Generation)).SingleLine().TextColor(p.Muted)
			state := locales.Text("正在观察原 HTTP 范围")
			if w.status.Ended {
				state = locales.Text("原 HTTP Watch 已停止")
			}
			ui.Text(c, state).Bold().TextColor(p.Text)
			ui.Text(c, fmt.Sprintf(locales.Text("原目标 %s · 实际读取前缀 %s"), strconv.Quote(w.request.Target), strconv.Quote(w.request.ObservationPrefix))).SingleLine().Tooltip(clipped(fmt.Sprintf("%q · %q", w.request.Target, w.request.ObservationPrefix)))
			ui.Text(c, fmt.Sprintf(locales.Text("已保留 %d / %d 笔 · %d / %d 字节"), w.status.Observations, w.request.MaxObservations, w.status.RetainedBytes, w.request.MaxBytes)).SingleLine()
			if w.status.Terminal.Reason != "" {
				ui.Text(c, w.status.Terminal.Reason).SingleLine().Tooltip(clipped(w.status.Terminal.Message)).TextColor(p.Warning)
			}
			if w.status.TerminalOverflow {
				ui.Text(c, locales.Text("已超作者预算；完整终末响应单独保留，读取已停止")).SingleLine().Tooltip(locales.Text("已超作者预算；完整终末响应单独保留，读取已停止")).TextColor(p.Warning)
			}
			if w.aclFiltered {
				ui.Text(c, locales.Text("原读取被 ACL 过滤；没有匹配项不能证明键已删除")).SingleLine().Tooltip(locales.Text("原读取被 ACL 过滤；没有匹配项不能证明键已删除")).TextColor(p.Warning)
			}
			if w.incomplete {
				ui.Text(c, locales.Text("原 HTTP 捕获或投影不完整；保留实际响应证据")).SingleLine().Tooltip(locales.Text("原 HTTP 捕获或投影不完整；保留实际响应证据")).TextColor(p.Warning)
			}
			if w.indexRelation == "zero" || w.indexRelation == "backward" || w.indexRelation == "unavailable" || w.indexRelation == "invalid" {
				ui.Text(c, locales.Text("原 index 为零、回退、缺失或非法；没有自动重置或重订阅")).SingleLine().Tooltip(locales.Text("原 index 为零、回退、缺失或非法；没有自动重置或重订阅")).TextColor(p.Warning)
			}
			if w.status.Observations > 0 {
				options := make([]string, w.status.Observations)
				for i := range options {
					options[i] = strconv.Itoa(i + 1)
				}
				choice := strconv.Itoa(w.selected + 1)
				if ui.Select(c, &choice, options).Label(locales.Text("原观察序号")).Width(150).Changed() {
					i, err := strconv.Atoi(choice)
					if err == nil {
						s.SelectHTTPWatchObservation(d, i-1)
					}
				}
			}
			ui.Text(c, locales.Text("预览有界；完整复制和导出保留原状态、headers、body 与 typed entries")).SingleLine().Tooltip(locales.Text("预览有界；完整复制和导出保留原状态、headers、body 与 typed entries")).TextColor(p.Muted)
			preview := w.preview
			area := ui.TextArea(c, &preview).Label(locales.Text("HTTP Watch 原观察")).ReadOnly(true).FillWidth().Height(160)
			theme.BindNativeEdit(c, area, fmt.Sprintf("kv/http-watch/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
		})
	})
}
