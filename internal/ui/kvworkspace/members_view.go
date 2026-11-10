package kvworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func memberStateLabel(state string) string {
	switch state {
	case "succeeded":
		return locales.Text("已成功")
	case "failed":
		return locales.Text("已失败")
	case "unknown":
		return locales.Text("未确认")
	default:
		return locales.Text("未执行")
	}
}

func (s *State) membersView(c *ui.Context, d *Document) {
	m := d.memberState()
	ready := !d.Busy() && !d.NeedsRecovery() && d.MembersAvailable()
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Padding(4, 8).Gap(4).Children(func() {
		if m.showResults {
			ui.Text(c, locales.Text("原成员选择与请求文本保留")).SingleLine().TextColor(p.Muted)
			if ui.Button(c, locales.Text("编辑成员选择")).Disabled(d.Busy()).Clicked() {
				m.showResults = false
				s.MembersSplitPercent = 60
			}
			return
		}
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("读取成员目录")).Disabled(!ready).Clicked() {
				s.ObserveMembers(d)
			}
			if ui.Button(c, locales.Text("检查所选成员")).Disabled(!ready || !memberDirectoryVisibleReady(d)).Clicked() {
				s.BindMembers(d)
			}
			label := locales.Text("选择 JSON")
			if m.showJSON {
				label = locales.Text("成员列表")
			}
			if ui.Button(c, label).Clicked() {
				m.showJSON = !m.showJSON
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if m.showJSON {
			d.Editor.Label = locales.Text("KV JSON 编辑器")
			d.Editor.ReadOnly = d.Busy()
			e := editor.View(c, d.Editor).Fill().MinHeight(0)
			if s.deps.EditFocus != nil {
				e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
			}
			handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
			e.Bind(handle)
			s.registerEditor(d, e.Focused(), handle)
			return
		}
		if m.directory == nil {
			ui.Text(c, locales.Text("先读取原连接的完整成员目录")).TextColor(p.Muted)
			return
		}
		targets, parseErr := parseMemberTargets(d.Request(), false)
		if parseErr != nil {
			ui.Text(c, locales.Text("成员选择 JSON 无效")).TextColor(p.Warning).SingleLine()
		}
		ui.Text(c, fmt.Sprintf(locales.Text("%d 个目录成员 · %d 个明确目标"), len(m.directory.Members), len(targets))).SingleLine().TextColor(p.Muted)
		previous := m.row
		m.columns.Selected = &m.row
		width, height := c.Size()
		if m.row >= 0 && (m.viewWidth != width || m.viewHeight != height) {
			m.columns.ScrollIntoView(m.row)
		}
		m.viewWidth, m.viewHeight = width, height
		theme.Table(c, &m.columns, []ui.TableColumn{{Title: locales.Text("批次"), Width: 55}, {Title: locales.Text("成员 ID"), Width: 215}, {Title: locales.Text("名称"), Width: 110}, {Title: locales.Text("成员角色"), Width: 95}, {Title: "Client URLs", Width: 300}}, len(m.directory.Members), func(row, col int) {
			member := m.directory.Members[row]
			text := ""
			switch col {
			case 0:
				for _, target := range targets {
					if target.MemberID == member.ID {
						text = "✓"
						break
					}
				}
			case 1:
				text = strconv.FormatUint(member.ID, 10)
			case 2:
				text = member.Name
			case 3:
				text = "voter"
				if member.IsLearner {
					text = "learner"
				}
			case 4:
				text = strings.Join(member.ClientURLs, " · ")
			}
			ui.Text(c, clipped(text)).SingleLine().Tooltip(clipped(text))
		}).Label(locales.Text("原集群成员列表")).Fill().MinHeight(45)
		if m.row != previous || m.editRow != m.row {
			m.editRow = m.row
			m.endpoint, m.version = "", ""
			if m.row >= 0 && m.row < len(m.directory.Members) {
				member := m.directory.Members[m.row]
				if len(member.ClientURLs) > 0 {
					m.endpoint = member.ClientURLs[0]
				}
				for _, target := range targets {
					if target.MemberID == member.ID {
						m.endpoint, m.version = target.Endpoint, target.Version
						break
					}
				}
			}
			d.confirmation = nil
		}
		if m.row < 0 || m.row >= len(m.directory.Members) {
			return
		}
		member := m.directory.Members[m.row]
		selected := false
		for _, target := range targets {
			if target.MemberID == member.ID {
				selected = true
				break
			}
		}
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(5).Children(func() {
			ui.Text(c, "Endpoint").SingleLine()
			changed := ui.TextInput(c, &m.endpoint).Label(locales.Text("明确成员 endpoint")).Grow(1).MinWidth(90).Disabled(d.Busy()).Changed()
			ui.Text(c, locales.Text("版本")).SingleLine()
			changed = ui.TextInput(c, &m.version).Label(locales.Text("此成员预期版本")).Width(90).Disabled(d.Busy()).Changed() || changed
			if changed {
				d.confirmation, d.permit = nil, nil
				if selected {
					s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: member.ID, Endpoint: m.endpoint, Version: m.version}, true)
				}
			}
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Checkbox(c, &selected, locales.Text("选择此原成员")).Tooltip(locales.Text("learner 可检查 Status；Defragment 批次拒绝 learner")).Disabled(d.Busy() || parseErr != nil || !selected && (m.endpoint == "" || m.version == "")).Changed() {
				s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: member.ID, Endpoint: m.endpoint, Version: m.version}, selected)
			}
			index := -1
			if m.bindings != nil {
				for i, entry := range m.bindings.entries {
					if entry.receipt.Target.MemberID == member.ID && entry.lease != nil {
						index = i
						break
					}
				}
			}
			if ui.Button(c, locales.Text("读取原成员 Status")).Disabled(!ready || index < 0).Clicked() {
				s.ObserveMemberStatus(d, index)
			}
		})
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareMemberBatch(d) })
	})
}

func (s *State) membersReceiptView(c *ui.Context, d *Document) {
	m := d.memberState()
	p := theme.Colors(c)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制完整成员记录")).Disabled(!d.memberContent() || d.FileBusy()).Clicked() {
			raw, err := s.MembersRecord(d)
			if err == nil {
				c.WriteClipboard(string(raw))
			}
		}
		if ui.Button(c, locales.Text("导出成员记录")).Disabled(!d.memberContent() || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
			s.ExportMembersRecord(d)
		}
		if m.unknown != nil && ui.Button(c, locales.Text("原未确认成员回执")).Clicked() {
			m.showUnknown, m.recordView = true, 2
		}
	}).FillWidth().MinWidth(0).Padding(0).Gap(5)
	if !d.memberContent() {
		ui.Text(c, locales.Text("成员观察与逐步回执显示在此处")).TextColor(p.Muted)
		return
	}
	sources := []string{"完整成员记录", "成员逐步结果", "原生 ACK", "派发前 Status", "最近成员 Status", "成员握手"}
	options := make([]string, len(sources))
	for i, source := range sources {
		options[i] = locales.Text(source)
	}
	if m.recordView < 0 || m.recordView >= len(options) {
		m.recordView = 0
	}
	choice := options[m.recordView]
	if ui.Select(c, &choice, options).Label(locales.Text("成员证据视图")).FillWidth().Changed() {
		for i, option := range options {
			if choice == option {
				m.recordView = i
				if i != 2 && i != 3 {
					m.showUnknown = false
				}
				break
			}
		}
	}
	if m.recordView == 1 {
		if m.batch == nil {
			ui.Text(c, locales.Text("目标尚未执行"))
			return
		}
		batch := m.batch
		ui.Text(c, fmt.Sprintf(locales.Text("原连接 #%d · 原代次 %d · %d 个批次目标"), batch.Scope.Namespace.ProfileID, batch.Scope.Generation, len(batch.Targets))).SingleLine()
		m.resultColumns.Selected = &m.resultRow
		theme.Table(c, &m.resultColumns, []ui.TableColumn{{Title: locales.Text("原成员 ID"), Width: 220}, {Title: locales.Text("结果"), Width: 90}, {Title: "Code", Width: 120}}, len(batch.Targets), func(row, col int) {
			target := batch.Targets[row]
			text := strconv.FormatUint(target.Scope.Member.ID, 10)
			if col == 1 {
				text = memberStateLabel(target.State)
			}
			if col == 2 {
				text = ""
				if target.Result != nil {
					text = target.Result.Result.Code
				}
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Label(locales.Text("原成员逐步结果")).Fill().MinHeight(45)
		return
	}
	if m.recordView == 0 {
		if !m.previewCaptured || m.previewRevision != m.revision {
			raw, err := s.MembersRecord(d)
			m.previewError = ""
			if err != nil {
				m.previewError = err.Error()
			}
			m.preview = clipped(string(raw))
			m.previewRevision, m.previewCaptured = m.revision, true
		}
		if m.previewError != "" {
			ui.Text(c, clipped(m.previewError)).TextColor(p.Warning)
			return
		}
		text := m.preview
		area := ui.TextArea(c, &text).Label(locales.Text("完整成员记录预览")).ReadOnly(true).Fill().MinHeight(0)
		theme.BindNativeEdit(c, area, fmt.Sprintf("kv/members/record/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
		return
	}
	scope, result, nativeError, ok := memberVisibleNative(d, m.recordView)
	if !ok {
		ui.Text(c, locales.Text("目标尚未执行")).TextColor(p.Muted)
		return
	}
	label := fmt.Sprintf(locales.Text("原成员 %s · 原代次 %d · %s"), strconv.FormatUint(scope.Member.ID, 10), scope.Original.Generation, result.Code)
	ui.Text(c, label).SingleLine().Tooltip(label)
	ui.Text(c, ReceiptStatus(result)).SingleLine().TextColor(p.Muted)
	text := result.JSON
	if text == "" {
		text = nativeError
	}
	if text == "" {
		text = ReceiptStatus(result)
	}
	text = clipped(text)
	area := ui.TextArea(c, &text).Label(locales.Text("成员原生观察")).ReadOnly(true).Fill().MinHeight(0)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/members/native/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}

func memberVisibleNative(d *Document, kind int) (contract.KVMemberScope, contract.KVResult, string, bool) {
	m := d.members
	if m == nil {
		return contract.KVMemberScope{}, contract.KVResult{}, "", false
	}
	if kind == 2 || kind == 3 {
		var r *contract.KVMemberResult
		if m.showUnknown {
			r = m.unknown
		} else if m.batch != nil && m.resultRow >= 0 && m.resultRow < len(m.batch.Targets) {
			r = m.batch.Targets[m.resultRow].Result
		}
		if r == nil {
			return contract.KVMemberScope{}, contract.KVResult{}, "", false
		}
		if kind == 3 {
			return r.Scope, r.Before, r.BeforeError, true
		}
		return r.Scope, r.Result, r.Error, true
	}
	selectedID := uint64(0)
	if m.directory != nil && m.row >= 0 && m.row < len(m.directory.Members) {
		selectedID = m.directory.Members[m.row].ID
	}
	if kind == 4 {
		for i := len(m.statuses) - 1; i >= 0; i-- {
			r := m.statuses[i]
			if selectedID == 0 || r.Scope.Member.ID == selectedID {
				return r.Scope, r.Result, r.Error, true
			}
		}
	}
	if kind == 5 {
		for i := len(m.history) - 1; i >= 0; i-- {
			r := m.history[i]
			if selectedID == 0 || r.Scope.Member.ID == selectedID {
				return r.Scope, r.Scope.Handshake, r.Error, true
			}
		}
	}
	return contract.KVMemberScope{}, contract.KVResult{}, "", false
}

func (s *State) memberConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	b := p.members.receipt
	colors := theme.Colors(c)
	ui.Column(c).Width(560).Gap(5).Children(func() {
		ui.Text(c, locales.Text("确认原成员 Defragment 批次")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("原连接 #%d · 原代次 %d · %d 个批次目标"), b.Scope.Namespace.ProfileID, b.Scope.Generation, len(b.Targets))).SingleLine()
		state := &d.memberState().confirmationColumns
		theme.Table(c, state, []ui.TableColumn{{Title: locales.Text("原成员 ID"), Width: 210}, {Title: "Endpoint", Width: 240}, {Title: locales.Text("版本"), Width: 95}}, len(b.Targets), func(row, col int) {
			t := b.Targets[row]
			text := strconv.FormatUint(t.Scope.Member.ID, 10)
			if col == 1 {
				text = t.Scope.Endpoint
			}
			if col == 2 {
				text = t.Scope.Version
			}
			ui.Text(c, clipped(text)).SingleLine().Tooltip(clipped(text))
		}).Label(locales.Text("不可变原成员批次")).Height(105).FillWidth()
		for _, source := range []string{"每步重新观察 leader；先 follower，后所选 leader", "首次失败或未确认即停止；其余目标保持未执行", "领导者观察不是锁；取消不代表回滚，不会自动重放"} {
			label := locales.Text(source)
			ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Warning)
		}
		ui.Text(c, "/etcdserverpb.Maintenance/Defragment  {}").SingleLine()
		if ui.Button(c, locales.Text("复制完整成员批次预览")).Clicked() {
			v, err := memberBatchData(&b)
			if err == nil {
				raw, err := marshalMemberPreview(v)
				if err == nil {
					c.WriteClipboard(string(raw))
				}
			}
		}
		ui.Checkbox(c, &p.checked, locales.Text("我确认执行此不可变原成员批次"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认执行 Defragment 批次")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}
