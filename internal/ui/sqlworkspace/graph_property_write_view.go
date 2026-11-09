package sqlworkspace

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) graphPropertyControls(c *ui.Context, d *Document) {
	if d == nil || !d.cypher() {
		return
	}
	if d.graphPropertyBytes >= graphPropertyRecordBudget-(4<<20) || len(d.graphPropertyRecords) >= 1000 {
		ui.Text(c, locales.Text("属性操作记录预算不足；已有记录可继续查看和导出")).SingleLine().TextColor(theme.Colors(c).Warning)
	}
	if d.graphPropertyDraft != nil {
		if ui.Button(c, locales.Text("图属性草稿")).Clicked() {
			d.graphPropertyOpen = true
			d.graphPropertyRecord = -1
		}
	}
	if len(d.graphPropertyRecords) > 0 {
		ui.Button(c, fmt.Sprintf(locales.Text("属性操作记录 · %d"), len(d.graphPropertyRecords))).Menu(func(m *ui.Menu) {
			for index, r := range d.graphPropertyRecords {
				if m.Item(fmt.Sprintf("%d · %s", index+1, graphLabel(r.Request.Target.Kind+" · "+r.Request.Property, 50))).Chosen() {
					d.graphPropertyRecord = index
					d.graphPropertyOpen = true
				}
			}
		})
	}
}
func (s *State) graphPropertyModals(c *ui.Context) {
	for _, owner := range s.documents {
		if !s.active(owner) || s.persistence.graphLeaveDoc != nil {
			continue
		}
		s.graphPropertyDialog(c, owner)
		for index, snapshot := range owner.snapshots {
			s.graphPropertyDialog(c.Key(fmt.Sprintf("graph-property-snapshot-%d", index)), snapshot.Document)
		}
	}
	s.graphPropertyLeaveModal(c)
}
func (s *State) graphPropertyDialog(c *ui.Context, d *Document) {
	if d.graphPropertyOpen {
		theme.Modal(c.Key(fmt.Sprintf("graph-property-%d", d.id)), &d.graphPropertyOpen, func() {
			_, height := c.Size()
			ui.Column(c).Width(560).Gap(7).Padding(10).Children(func() {
				ui.Scroll(c).FillWidth().MinHeight(0).MaxHeight(max(float32(120), height-160)).Children(func() {
					ui.Column(c).Gap(7).Children(func() {
						if d.graphPropertyDraft != nil && d.graphPropertyRecord < 0 {
							s.graphPropertyDraftView(c, d)
						} else {
							s.graphPropertyRecordView(c, d)
						}
					})
				})
				ui.Row(c).Gap(6).Children(func() {
					if d.graphPropertyPending != nil && ui.Button(c, locales.Text("取消图属性操作")).Disabled(d.graphPropertyPending.canceled).Clicked() {
						s.cancelGraphProperty(d)
					}
					if ui.Button(c, locales.Text("导出属性操作 JSON")).Disabled(len(d.graphPropertyRecords) == 0 || s.persistence.files.SavePath == nil || s.persistence.files.WriteStream == nil).Clicked() {
						s.exportGraphPropertyRecords(d)
					}
					if ui.Button(c, locales.Text("关闭")).Clicked() {
						d.graphPropertyOpen = false
					}
				})
				if d.graphPropertyPending != nil {
					ui.Text(c, locales.Text("等待原执行器确认；不会自动重试或重放")).TextColor(theme.Colors(c).Warning)
					c.AnimationFrame()
				}
			})
		})
	}
}
func (s *State) graphPropertyDraftView(c *ui.Context, d *Document) {
	p := d.graphPropertyDraft
	if d.graphPropertyBytes >= graphPropertyRecordBudget-(4<<20) || len(d.graphPropertyRecords) >= 1000 {
		ui.Text(c, locales.Text("属性操作记录预算不足；已有记录可继续查看和导出")).SingleLine().TextColor(theme.Colors(c).Warning)
	}
	caption, confirm := "编辑图属性", "确认保存图属性"
	if p.create {
		caption, confirm = "新增图属性", "确认新增图属性"
	}
	if p.remove {
		caption, confirm = "移除图属性", "确认移除图属性"
	}
	ui.Text(c, locales.Text(caption)).Bold()
	ui.Text(c, p.request.Target.Kind+" · "+p.name+" · "+p.kind).SingleLine().Tooltip(p.request.Target.ElementID)
	if p.create {
		ui.Text(c, locales.Text("仅当该属性仍不存在时新增；空字符串也是一个已存在的属性值")).SingleLine().Tooltip(locales.Text("仅当该属性仍不存在时新增；空字符串也是一个已存在的属性值"))
	} else {
		ui.Text(c, fmt.Sprintf(locales.Text("保留原值：%s"), graphPropertyPreview(p.request.Before))).SingleLine()
	}
	if p.remove {
		ui.Text(c, locales.Text("确认后图元将不再包含此属性；其他属性保留")).SingleLine().Tooltip(locales.Text("确认后图元将不再包含此属性；其他属性保留"))
	}
	ui.Text(c, locales.Text("保存会锁定目标并比较属性旧值和类型；其他属性不参与比较")).SingleLine().Tooltip(locales.Text("保存会锁定目标并比较属性旧值和类型；其他属性不参与比较"))
	changed := false
	if p.create {
		changed = ui.TextInput(c, &p.name).Label(locales.Text("图属性名称")).MaxLength(4096).Disabled(p.attempted || d.Loading()).Changed()
		kinds := []string{"string", "boolean", "integer", "float"}
		labels := []string{locales.Text("字符串"), locales.Text("布尔值"), locales.Text("整数"), locales.Text("浮点数")}
		choice := labels[0]
		for i, kind := range kinds {
			if p.kind == kind {
				choice = labels[i]
			}
		}
		if ui.Select(c, &choice, labels).Label(locales.Text("图属性类型")).Disabled(p.attempted || d.Loading()).Changed() {
			for i, label := range labels {
				if choice == label {
					p.kind = kinds[i]
				}
			}
			if p.kind == "boolean" && p.text != "true" && p.text != "false" {
				p.text = "false"
			}
			changed = true
		}
	}
	if !p.remove && p.kind == "boolean" {
		value := p.text == "true"
		if ui.Checkbox(c, &value, locales.Text("属性新值")).Disabled(p.attempted || d.Loading()).Changed() {
			p.text = strconv.FormatBool(value)
			changed = true
		}
	} else if !p.remove {
		changed = ui.TextArea(c, &p.text).Label(locales.Text("属性新值")).MaxLength(1<<20).ReadOnly(p.attempted || d.Loading()).Height(80).Changed() || changed
	}
	if changed {
		p.preview = nil
		p.error = ""
		p.notice = ""
	}
	ui.Row(c).Gap(6).Children(func() {
		if ui.Button(c, locales.Text("预览属性保存")).Disabled(p.attempted || d.Loading()).Clicked() {
			s.previewGraphProperty(d)
		}
		if ui.PrimaryButton(c, locales.Text(confirm)).Disabled(p.preview == nil || p.attempted || !s.graphPropertyLeaseReady(d)).Clicked() {
			s.saveGraphProperty(d)
		}
		if ui.Button(c, locales.Text("放弃属性草稿")).Disabled(d.graphPropertyPending != nil).Clicked() {
			d.graphPropertyDraft = nil
			d.graphPropertyRecord = len(d.graphPropertyRecords) - 1
			if d.graphPropertyRecord < 0 {
				d.graphPropertyOpen = false
			}
		}
	})
	if p.preview != nil {
		raw, _ := json.MarshalIndent(p.preview, "", "  ")
		text := string(raw)
		area := ui.TextArea(c, &text).Label(locales.Text("完整图属性保存请求 JSON")).ReadOnly(true).Height(120)
		reading := ui.Local(area, "graph-property-request-reading", func() *contract.GraphPropertyWriteRequest { return nil })
		if *reading != p.preview {
			area.SetTextSelection(0, 0)
			*reading = p.preview
		}
	}
	message := p.error
	if p.notice != "" {
		message = p.notice
	}
	if message != "" {
		text := locales.Text(message)
		ui.Text(c, text).SingleLine().Tooltip(text).TextColor(theme.Colors(c).Warning)
	}
	if p.attempted && len(d.graphPropertyRecords) > 0 {
		if ui.Button(c, locales.Text("查看属性操作回执")).Clicked() {
			d.graphPropertyRecord = len(d.graphPropertyRecords) - 1
		}
	}
}
func (s *State) graphPropertyRecordView(c *ui.Context, d *Document) {
	index := d.graphPropertyRecord
	ui.Text(c, locales.Text("属性操作原生回执")).Bold()
	if index < 0 || index >= len(d.graphPropertyRecords) {
		return
	}
	r := d.graphPropertyRecords[index]
	status := "原生回执未确认属性替换"
	if r.Request.Before.Kind == "null" {
		status = "原生回执未确认属性新增"
	}
	if r.Request.After.Kind == "null" {
		status = "原生回执未确认属性移除"
	}
	if r.Receipt.Applied {
		status = "原生回执确认属性已替换"
		if r.Request.Before.Kind == "null" {
			status = "原生回执确认属性已新增"
		}
		if r.Request.After.Kind == "null" {
			status = "原生回执确认属性已移除"
		}
	}
	if r.Receipt.Conflict {
		status = "属性或图元已变化；未替换目标值，请显式核对当前图元"
		if r.Request.Before.Kind == "null" || r.Request.After.Kind == "null" {
			status = "属性或图元已变化；本次新增或移除未被接纳，请显式核对当前图元"
		}
	}
	if r.Verification {
		status = "已保留独立图元核对结果"
		if !r.Receipt.Result.Completed || r.Receipt.Result.Uncertain || r.Receipt.Result.Truncated {
			status = "图元核对未获得完整原生确认"
		} else if len(r.Receipt.Result.Rows) == 0 {
			status = "该次核对未返回图元；此前图形仍是旧观察"
		}
	}
	ui.Text(c, locales.Text(status)).SingleLine().Tooltip(locales.Text(status))
	raw, _ := json.MarshalIndent(r, "", "  ")
	text := string(raw)
	area := ui.TextArea(c, &text).Label(locales.Text("完整图属性操作记录 JSON")).ReadOnly(true).Height(230)
	reading := ui.Local(area, "graph-property-record-reading", func() struct {
		Owner *Document
		Index int
	} {
		return struct {
			Owner *Document
			Index int
		}{}
	})
	if reading.Owner != d || reading.Index != index {
		area.SetTextSelection(0, 0)
		reading.Owner, reading.Index = d, index
	}
	ui.Text(c, locales.Text("读回只代表该次新观察；匹配值不能补充缺失的写入确认")).SingleLine().Tooltip(locales.Text("读回只代表该次新观察；匹配值不能补充缺失的写入确认"))
	ui.Row(c).Gap(6).Children(func() {
		if ui.Button(c, locales.Text("核对当前图元")).Disabled(!s.graphPropertyLeaseReady(d) || r.Request.Namespace != d.Namespace()).Clicked() {
			s.verifyGraphProperty(d, index)
		}
		if d.graphPropertyDraft != nil && ui.Button(c, locales.Text("返回属性草稿")).Clicked() {
			d.graphPropertyRecord = -1
		}
	})
}
func (s *State) guardGraphPropertyLeave(d *Document, next func(ui.Services)) bool {
	if d == nil || d.closed || d.graphPropertyDraft == nil && d.graphPropertyPending == nil {
		return false
	}
	s.persistence.graphLeaveDoc, s.persistence.graphLeaveNext = d, next
	return true
}
func (s *State) graphPropertyLeaveModal(c *ui.Context) {
	p := &s.persistence
	d := p.graphLeaveDoc
	if d == nil {
		return
	}
	open := true
	theme.Modal(c.Key("graph-property-leave"), &open, func() {
		ui.Column(c).Width(460).Gap(10).Children(func() {
			ui.Text(c, locales.Text("图属性草稿或操作尚未处理"))
			ui.Text(c, locales.Text("放弃草稿不会证明服务器端写入已回滚；原生回执可独立导出")).SingleLine().Tooltip(locales.Text("放弃草稿不会证明服务器端写入已回滚；原生回执可独立导出"))
			ui.Row(c).Gap(7).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					p.graphLeaveDoc, p.graphLeaveNext = nil, nil
				}
				if ui.Button(c, locales.Text("查看属性操作回执")).Clicked() {
					d.graphPropertyOpen = true
					d.graphPropertyRecord = len(d.graphPropertyRecords) - 1
					p.graphLeaveDoc, p.graphLeaveNext = nil, nil
				}
				if d.graphPropertyPending != nil && ui.Button(c, locales.Text("取消图属性操作")).Disabled(d.graphPropertyPending.canceled).Clicked() {
					s.cancelGraphProperty(d)
				}
				if ui.Button(c, locales.Text("放弃属性草稿")).Disabled(d.graphPropertyPending != nil).Clicked() {
					next := p.graphLeaveNext
					p.graphLeaveDoc, p.graphLeaveNext = nil, nil
					d.graphPropertyDraft = nil
					d.graphPropertyOpen = false
					if !s.GuardLeave(d, next) && next != nil {
						next(c.Services())
					}
				}
			})
			if d.graphPropertyPending != nil {
				c.AnimationFrame()
			}
		})
	})
	if !open {
		p.graphLeaveDoc, p.graphLeaveNext = nil, nil
	}
}
