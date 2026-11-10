package kvworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) compactionView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	ready := !d.Busy() && !d.NeedsRecovery() && d.CompactionAvailable()
	ui.Column(c).Fill().MinHeight(0).Padding(5, 8).Gap(4).Children(func() {
		ui.Text(c, locales.Text("压缩集群历史")).Bold()
		revision, physical, err := compactionInputs(d.Request())
		if err != nil {
			ui.Text(c, locales.Text("Compact 请求仅接受 revision 和 physical")).TextColor(p.Warning).SingleLine().Tooltip(locales.Text("Compact 请求仅接受 revision 和 physical"))
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("压缩 revision")).SingleLine()
			if ui.TextInput(c, &revision).Label(locales.Text("压缩 revision")).Grow(1).MinWidth(80).Disabled(d.Busy()).Changed() {
				s.SetCompactionInputs(d, revision, physical)
			}
		})
		if ui.Checkbox(c, &physical, locales.Text("等待物理压缩完成")).Disabled(d.Busy()).Changed() {
			s.SetCompactionInputs(d, revision, physical)
		}
		ui.Text(c, locales.Text("整个集群历史；连接前缀不限定压缩范围")).SingleLine().Tooltip(locales.Text("整个集群历史；连接前缀不限定压缩范围")).TextColor(p.Warning)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("检查 revision")).Disabled(!ready).Clicked() {
				s.ObserveCompaction(d)
			}
			if ui.Button(c, locales.Text("预览 Compact")).Disabled(!ready || d.hasUnknownWrites() || !compactionObservationReady(d, d.compactionObservation)).Clicked() {
				s.PrepareCompaction(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("复制完整观察")).Disabled(d.compactionObservation == nil).Clicked() {
				raw, err := compactionObservationJSON(d.compactionObservation.Clone())
				if err == nil {
					c.WriteClipboard(string(raw))
				}
			}
			if ui.Button(c, locales.Text("导出 Compact 观察")).Disabled(d.compactionObservation == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
				s.ExportCompactionObservation(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if r := d.compactionObservation; r != nil {
			if len(r.Steps) > 0 {
				ui.Text(c, fmt.Sprintf(locales.Text("观察到的当前 revision：%s"), strconv.FormatInt(r.Steps[0].Result.Header.Revision, 10))).SingleLine()
			}
			code := locales.Text("读取未完成")
			if len(r.Steps) > 1 {
				code = r.Steps[1].Result.Code
			}
			label := fmt.Sprintf(locales.Text("原 revision %s · Range %s · 探测键 AA=="), strconv.FormatInt(r.Revision, 10), code)
			ui.Text(c, label).SingleLine().Tooltip(label)
			if !compactionObservationReady(d, r) {
				ui.Text(c, locales.Text("此前观察不能授权当前请求，请重新检查")).TextColor(p.Warning).SingleLine().Tooltip(locales.Text("此前观察不能授权当前请求，请重新检查"))
			}
		}
		ui.Text(c, locales.Text("观察不是锁；确认时仍由服务端决定")).SingleLine().Tooltip(locales.Text("观察不是锁；确认时仍由服务端决定")).TextColor(p.Muted)
		// Keep the existing JSON editor and caret/clipboard dispatch available.
		d.Editor.Label = locales.Text("KV JSON 编辑器")
		d.Editor.ReadOnly = d.Busy()
		e := editor.View(c, d.Editor).Fill().MinHeight(0)
		if s.deps.EditFocus != nil {
			e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
		}
		handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
		e.Bind(handle)
		s.registerEditor(d, e.Focused(), handle)
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareCompaction(d) })
	})
}

func (s *State) compactionConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	r := p.compaction
	colors := theme.Colors(c)
	ui.Column(c).Width(540).Gap(5).Children(func() {
		ui.Text(c, locales.Text("确认压缩原集群历史")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine()
		ui.Text(c, fmt.Sprintf(locales.Text("原集群 %s · 原文档 %s"), strconv.FormatUint(r.Scope.Header.ClusterID, 10), strconv.FormatUint(r.DocumentID, 10))).SingleLine()
		ui.Text(c, fmt.Sprintf(locales.Text("原成员 %s · revision %s · physical %s"), strconv.FormatUint(r.Scope.Header.MemberID, 10), strconv.FormatInt(r.Revision, 10), strconv.FormatBool(r.Physical))).SingleLine()
		for _, text := range []string{"整个集群历史；连接前缀不限定压缩范围", "当前值保留；历史不能恢复，physical 不等于 Defragment", "观察不是锁；确认时仍由服务端决定", "仅派发一次；取消不代表回滚，丢失回执后必须显式核对"} {
			label := locales.Text(text)
			ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Warning)
		}
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("复制完整 Compact 预览")).Clicked() {
				raw, err := compactionPreviewJSON(r.clone())
				if err == nil {
					c.WriteClipboard(string(raw))
				}
			}
			if ui.Button(c, locales.Text("复制完整请求")).Clicked() {
				request := p.plan.Snapshot()
				c.WriteClipboard(request.Method + "\n" + request.JSON)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		ui.Checkbox(c, &p.checked, locales.Text("我确认压缩此原集群的历史"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认执行 Compact")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}
