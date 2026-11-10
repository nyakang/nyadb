package kvworkspace

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func alarmPair(a contract.KVAlarm) [2]uint64 { return [2]uint64{a.MemberID, uint64(uint32(a.Type))} }

// Presentation caches only authored inputs. Execution gates still validate the
// complete original observation, immutable snapshots and owning physical lease.
func alarmPresentationInputs(d *Document) (int32, []contract.KVAlarm, error) {
	a := d.alarmState()
	request := d.Request()
	if !a.inputCaptured || a.inputRequest != request {
		a.inputRequest, a.inputCaptured = request, true
		a.inputFilter, a.inputTargets, a.inputError = parseAlarmInputs(request)
		if a.authority != nil && a.observation != nil && a.inputError == nil && a.inputFilter != a.observation.Filter {
			d.releaseAlarmAuthority()
		}
	}
	return a.inputFilter, a.inputTargets, a.inputError
}
func alarmSelectionVisibleReady(d *Document) bool {
	a := d.alarms
	if a == nil || a.authority == nil || a.authority.lease != d.lease || a.authority.observation != a.observation || a.observation == nil || a.observation.DocumentID != d.id || !sameEventScope(a.observation.Scope, d.scope) {
		return false
	}
	filter, targets, err := alarmPresentationInputs(d)
	if err != nil || a.authority == nil || filter != a.observation.Filter || len(targets) == 0 || !a.observation.Receipt.Result.Completed || !a.observation.Receipt.Result.NativeCaptured {
		return false
	}
	for _, target := range targets {
		if !a.observedPairs[alarmPair(target)] {
			return false
		}
	}
	return true
}

func (s *State) alarmsView(c *ui.Context, d *Document) {
	a := d.alarmState()
	ready := !d.Busy() && !d.NeedsRecovery() && d.AlarmsAvailable()
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Padding(4, 8).Gap(4).Children(func() {
		filter, targets, parseErr := alarmPresentationInputs(d)
		ui.Row(c).FillWidth().MinWidth(0).Gap(5).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, locales.Text("读取告警列表")).Disabled(!ready || parseErr != nil).Clicked() {
				s.ObserveAlarms(d)
			}
			options := []string{locales.Text("全部告警"), "NOSPACE", "CORRUPT"}
			choice := options[0]
			if filter >= 0 && filter < 3 {
				choice = options[filter]
			}
			if ui.Select(c, &choice, options).Label(locales.Text("告警类型过滤")).Width(140).Disabled(d.Busy()).Changed() {
				for i, option := range options {
					if choice == option {
						s.SetAlarmSelection(d, int32(i), nil)
					}
				}
			}
			label := locales.Text("选择 JSON")
			if a.showJSON {
				label = locales.Text("告警列表")
			}
			if ui.Button(c, label).Clicked() {
				a.showJSON = !a.showJSON
			}
		})
		if a.showJSON {
			d.Editor.Label, d.Editor.ReadOnly = locales.Text("KV JSON 编辑器"), d.Busy()
			e := editor.View(c, d.Editor).Fill().MinHeight(0)
			if s.deps.EditFocus != nil {
				e.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
			}
			handle := ui.Local(e, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
			e.Bind(handle)
			s.registerEditor(d, e.Focused(), handle)
			return
		}
		if parseErr != nil {
			ui.Text(c, locales.Text("告警选择 JSON 无效")).TextColor(p.Warning).SingleLine()
		}
		if a.observation == nil || a.observation.Receipt.Result.Reply == nil {
			ui.Text(c, locales.Text("请先读取原连接的告警列表")).TextColor(p.Muted)
			return
		}
		observation := a.observation
		alarms := observation.Receipt.Result.Reply.Alarms
		label := fmt.Sprintf(locales.Text("原集群 %s · %d 个告警 · %d 个选择"), strconv.FormatUint(observation.Scope.Header.ClusterID, 10), len(alarms), len(targets))
		ui.Text(c, label).SingleLine().Tooltip(label).TextColor(p.Muted)
		a.columns.Selected = &a.row
		width, height := c.Size()
		if a.row >= 0 && (a.viewWidth != width || a.viewHeight != height) {
			a.columns.ScrollIntoView(a.row)
		}
		a.viewWidth, a.viewHeight = width, height
		theme.Table(c, &a.columns, []ui.TableColumn{{Title: locales.Text("批次"), Width: 55}, {Title: locales.Text("原成员 ID"), Width: 230}, {Title: locales.Text("告警类型"), Width: 145}}, len(alarms), func(row, col int) {
			alarm := alarms[row]
			text := ""
			switch col {
			case 0:
				for _, target := range targets {
					if alarmPair(target) == alarmPair(alarm) {
						text = "✓"
						break
					}
				}
			case 1:
				text = strconv.FormatUint(alarm.MemberID, 10)
			case 2:
				text = alarmTypeName(alarm.Type)
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Label(locales.Text("原集群告警列表")).Fill().MinHeight(45)
		if a.row >= 0 && a.row < len(alarms) {
			alarm := alarms[a.row]
			selected := false
			for _, target := range targets {
				selected = selected || alarmPair(target) == alarmPair(alarm)
			}
			if ui.Checkbox(c, &selected, locales.Text("选择此原告警")).Disabled(!ready || parseErr != nil || alarm.Type != 1 && alarm.Type != 2 || !selected && len(targets) >= 8).Changed() {
				updated := make([]contract.KVAlarm, 0, len(targets)+1)
				for _, target := range targets {
					if alarmPair(target) != alarmPair(alarm) {
						updated = append(updated, target)
					}
				}
				if selected {
					updated = append(updated, alarm)
				}
				s.SetAlarmSelection(d, filter, updated)
			}
		}
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareAlarmBatch(d) })
	})
}

func alarmVisibleReceipt(d *Document, kind int) *Receipt {
	a := d.alarms
	if a == nil {
		return nil
	}
	if kind == 2 && a.observation != nil {
		return &a.observation.Receipt
	}
	if kind == 3 && a.showUnknown {
		return a.unknown
	}
	if kind == 3 || kind == 4 {
		if a.batch != nil && a.resultRow >= 0 && a.resultRow < len(a.batch.Targets) {
			if kind == 4 {
				return a.batch.Targets[a.resultRow].Before
			}
			return a.batch.Targets[a.resultRow].Result
		}
	}
	if kind == 5 && len(a.recovered) > 0 {
		return &a.recovered[len(a.recovered)-1]
	}
	return nil
}

func (s *State) alarmsReceiptView(c *ui.Context, d *Document) {
	a := d.alarmState()
	p := theme.Colors(c)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制完整告警记录")).Disabled(!d.alarmContent() || d.FileBusy()).Clicked() {
			raw, err := s.AlarmsRecord(d)
			if err == nil {
				c.WriteClipboard(string(raw))
			}
		}
		if ui.Button(c, locales.Text("导出告警记录")).Disabled(!d.alarmContent() || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
			s.ExportAlarmsRecord(d)
		}
		if a.unknown != nil && ui.Button(c, locales.Text("原未确认告警回执")).Clicked() {
			a.showUnknown, a.recordView = true, 3
		}
	}).FillWidth().MinWidth(0).Padding(0).Gap(5)
	if !d.alarmContent() {
		ui.Text(c, locales.Text("告警观察与逐步回执显示在此处")).TextColor(p.Muted)
		return
	}
	sources := []string{"完整告警记录", "告警逐步结果", "最近告警 GET", "原 Disarm ACK", "派发前 GET", "恢复的原告警证据"}
	options := make([]string, len(sources))
	for i, source := range sources {
		options[i] = locales.Text(source)
	}
	if a.recordView < 0 || a.recordView >= len(options) {
		a.recordView = 0
	}
	choice := options[a.recordView]
	if ui.Select(c, &choice, options).Label(locales.Text("告警证据视图")).FillWidth().Changed() {
		for i, option := range options {
			if choice == option {
				a.recordView, a.showUnknown = i, false
			}
		}
	}
	if a.recordView == 1 {
		if a.batch == nil {
			ui.Text(c, locales.Text("目标尚未执行"))
			return
		}
		b := a.batch
		label := fmt.Sprintf(locales.Text("原连接 #%d · 原代次 %d · %d 个批次目标"), b.Scope.Namespace.ProfileID, b.Scope.Generation, len(b.Targets))
		ui.Text(c, label).SingleLine().Tooltip(label)
		a.resultColumns.Selected = &a.resultRow
		theme.Table(c, &a.resultColumns, []ui.TableColumn{{Title: locales.Text("原成员 ID"), Width: 215}, {Title: locales.Text("告警类型"), Width: 110}, {Title: locales.Text("结果"), Width: 120}, {Title: "Code", Width: 110}}, len(b.Targets), func(row, col int) {
			target := b.Targets[row]
			text := strconv.FormatUint(target.Alarm.MemberID, 10)
			if col == 1 {
				text = alarmTypeName(target.Alarm.Type)
			}
			if col == 2 {
				text = memberStateLabel(target.State)
			}
			if col == 3 {
				text = ""
				if target.Result != nil {
					text = target.Result.Result.Code
				}
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Label(locales.Text("原告警逐步结果")).Fill().MinHeight(45)
		return
	}
	if a.recordView == 0 {
		if !a.previewCaptured || a.previewRevision != a.revision {
			raw, err := s.AlarmsRecord(d)
			a.previewError = ""
			if err != nil {
				a.previewError = err.Error()
			}
			a.preview = clipped(string(raw))
			a.previewRevision, a.previewCaptured = a.revision, true
		}
		if a.previewError != "" {
			ui.Text(c, clipped(a.previewError)).TextColor(p.Warning)
			return
		}
		text := a.preview
		area := ui.TextArea(c, &text).Label(locales.Text("完整告警记录预览")).ReadOnly(true).Fill().MinHeight(0)
		theme.BindNativeEdit(c, area, fmt.Sprintf("kv/alarms/record/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
		return
	}
	r := alarmVisibleReceipt(d, a.recordView)
	if r == nil {
		ui.Text(c, locales.Text("目标尚未执行")).TextColor(p.Muted)
		return
	}
	label := fmt.Sprintf(locales.Text("原连接 #%d · 原代次 %d · %s"), r.Scope.Namespace.ProfileID, r.Scope.Generation, r.Result.Code)
	ui.Text(c, label).SingleLine().Tooltip(label)
	ui.Text(c, ReceiptStatus(r.Result)).SingleLine().TextColor(p.Muted)
	text := r.Result.JSON
	if text == "" {
		text = r.Error
	}
	if text == "" {
		text = ReceiptStatus(r.Result)
	}
	text = clipped(text)
	area := ui.TextArea(c, &text).Label(locales.Text("告警原生观察")).ReadOnly(true).Fill().MinHeight(0)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/alarms/native/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}

func (s *State) alarmConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	b := p.alarms.receipt
	colors := theme.Colors(c)
	ui.Column(c).Width(560).Gap(5).Children(func() {
		ui.Text(c, locales.Text("确认原告警 Disarm 批次")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("原连接 #%d · 原代次 %d · %d 个批次目标"), b.Scope.Namespace.ProfileID, b.Scope.Generation, len(b.Targets))).SingleLine()
		theme.Table(c, &d.alarmState().confirmationColumns, []ui.TableColumn{{Title: locales.Text("原成员 ID"), Width: 230}, {Title: locales.Text("告警类型"), Width: 180}}, len(b.Targets), func(row, col int) {
			target := b.Targets[row]
			text := strconv.FormatUint(target.Alarm.MemberID, 10)
			if col == 1 {
				text = alarmTypeName(target.Alarm.Type)
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Label(locales.Text("不可变原告警批次")).Height(105).FillWidth()
		for _, source := range []string{"每步重新读取完整告警列表并检查原目标", "首次失败或未确认即停止；其余目标保持未执行", "读取列表不会锁定告警；检查后告警仍可能变化或再次出现"} {
			label := locales.Text(source)
			ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Warning)
		}
		if ui.Button(c, locales.Text("复制完整告警批次预览")).Clicked() {
			v, err := alarmBatchData(&b)
			if err == nil {
				raw, err := json.MarshalIndent(v, "", "  ")
				if err == nil {
					c.WriteClipboard(string(raw))
				}
			}
		}
		ui.Checkbox(c, &p.checked, locales.Text("我确认执行此不可变原告警批次"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认执行 Disarm 批次")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}
