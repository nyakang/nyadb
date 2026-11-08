package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) settingsView(c *ui.Context, d *Document) {
	x := d.settings
	p := theme.Colors(c)
	ready := s.settingsReady(d)
	ui.Column(c).Fill().Background(p.App).Children(func() {
		ui.Row(c).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Children(func() {
			ui.Text(c, "Meilisearch · "+locales.Text("索引设置")+" · "+d.object.Catalog).SingleLine().Grow(1).MinWidth(0).Tooltip(d.object.Catalog)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("读取当前设置")).Disabled(!ready || x.receipt.Raw != "").Clicked() {
					s.LoadSettings(d, c.Services().Invalidate, true)
				}
				if ui.Button(c, locales.Text("编辑设置 JSON")).Disabled(!s.CanPrepareSettings(d)).Clicked() {
					s.EditSettings(d)
				}
				if ui.Button(c, locales.Text("预览设置更改")).Disabled(!s.CanPrepareSettings(d) || !x.dirty()).Clicked() {
					_ = s.PrepareSettings(d, false)
				}
				if ui.Button(c, locales.Text("预览重置设置")).Disabled(!s.CanPrepareSettings(d)).Clicked() {
					_ = s.PrepareSettings(d, true)
				}
				if ui.Button(c, locales.Text("放弃设置草稿")).Disabled(d.loading || !x.editing).Clicked() {
					s.discardSettingsDraft(d)
				}
				if ui.Button(c, locales.Text("恢复设置连接")).Disabled(d.loading || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected).Clicked() {
					s.RecoverSettings(d)
				}
				if d.loading && ui.Button(c, locales.Text("停止设置等待")).Disabled(x.cancelRequested).Clicked() {
					s.CancelSettings(d)
				}
				if ui.Button(c, locales.Text("复制当前完整设置")).Disabled(x.snapshot.Raw == "").Clicked() {
					c.WriteClipboard(x.snapshot.Raw)
				}
				if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
					s.deps.Back()
				}
			}).Label(locales.Text("索引设置操作")).Grow(1).MinWidth(0).Gap(4)
		})
		if d.loading {
			message := locales.Text("正在处理原设置请求…")
			if x.cancelRequested {
				message = locales.Text("正在等待原设置请求停止…")
			}
			ui.Text(c, message).SingleLine().Padding(2, 8).TextColor(p.Muted).Shrink(0)
		}
		if d.err != "" {
			ui.Text(c, locales.Text(d.err)).SingleLine().Padding(2, 8).TextColor(p.Muted).Tooltip(locales.Text(d.err)).Shrink(0)
		}
		if x.uncertain {
			ui.Text(c, locales.Text("设置提交结果未知；草稿和证据保留，不能重放。请检查原服务器任务。")).SingleLine().Padding(2, 8).Tooltip(locales.Text("设置提交结果未知；草稿和证据保留，不能重放。请检查原服务器任务。")).TextColor(p.Error).Shrink(0)
		}
		ui.Tabs(c, &x.tab, locales.Text("当前设置"), locales.Text("设置草稿"), locales.Text("设置任务与响应")).FillWidth().Shrink(0)
		if x.tab == 1 {
			ui.Text(c, locales.Text("草稿最多 1 MiB；只提交变化的字段，删除字段会提交 null 重置。请检查嵌套设置预览。")).SingleLine().Padding(2, 8).Tooltip(locales.Text("草稿最多 1 MiB；只提交变化的字段，删除字段会提交 null 重置。请检查嵌套设置预览。")).TextColor(p.Muted).Shrink(0)
			area := ui.TextArea(c.Key(fmt.Sprintf("settings-draft/%d", d.id)), &x.draft).Label(locales.Text("完整设置草稿")).ReadOnly(!x.editing || d.loading || x.preview || x.receipt.Raw != "" || x.uncertain).Fill().MinHeight(0)
			theme.BindNativeEdit(c, area, fmt.Sprintf("settings/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return d.SettingsMode() }, x.editing && !d.loading && !x.preview && x.receipt.Raw == "" && !x.uncertain, false)
		} else {
			raw, label := "", locales.Text("原生当前设置预览")
			if x.tab == 0 {
				raw = x.snapshot.Raw
			} else {
				label = locales.Text("原始设置任务证据预览")
				raw = x.last.Receipt.Raw
				if x.last.TaskRaw != "" {
					raw += "\n\n" + x.last.TaskRaw
				}
				if x.diagnostic != "" {
					raw += "\n\n" + x.diagnostic
				}
			}
			if raw == "" {
				ui.Text(c, locales.Text("请显式读取原索引设置或提交设置预览")).Padding(8)
				ui.Box(c).Grow(1).MinHeight(0)
			} else {
				preview, truncated := taskPreview(raw)
				if truncated {
					ui.Text(c, locales.Text("设置 JSON 预览已截断；可复制完整内容。")).SingleLine().Padding(2, 8).TextColor(p.Muted).Shrink(0)
				}
				area := ui.TextArea(c.Key(fmt.Sprintf("settings-preview/%d/%d", d.id, x.tab)), &preview).ReadOnly(true).Label(label).Fill().MinHeight(0)
				seen := ui.Local(area, "settings-preview-text", func() string { return "" })
				if *seen != preview {
					area.SetTextSelection(0, 0)
					*seen = preview
				}
			}
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).FillWidth().Padding(2, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			state := x.last.State
			label := taskStatusLabel(state)
			switch state {
			case "rejected":
				label = locales.Text("请求被拒绝")
			case "uncertain":
				label = locales.Text("结果未知")
			case "not_submitted":
				label = locales.Text("未提交")
			case "":
				label = "—"
			}
			ui.Text(c, label).Width(100).SingleLine().Tooltip(label)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("观察原设置任务（30 秒）")).Disabled(!ready || x.receipt.Raw == "").Clicked() {
					s.ObserveSettings(d, c.Services().Invalidate)
				}
				if ui.Button(c, locales.Text("复制完整设置回执")).Disabled(x.last.Receipt.Raw == "").Clicked() {
					c.WriteClipboard(x.last.Receipt.Raw)
				}
				if ui.Button(c, locales.Text("复制完整设置任务响应")).Disabled(x.last.TaskRaw == "" && x.diagnostic == "").Clicked() {
					raw := x.last.TaskRaw
					if x.diagnostic != "" {
						raw = x.diagnostic
					}
					c.WriteClipboard(raw)
				}
			}).Label(locales.Text("原设置任务操作")).Grow(1).MinWidth(0).Gap(4)
		})
	})
	if x.preview && x.plan != nil {
		theme.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				title := locales.Text("设置更改预览")
				if x.plan.Reset {
					title = locales.Text("重置全部索引设置预览")
				}
				ui.Text(c, title).Bold()
				ui.Text(c, d.object.Catalog+" · "+strings.Join(x.plan.Fields, ", ")).SingleLine().Tooltip(strings.Join(x.plan.Fields, ", "))
				ui.Text(c, locales.Text("提交前会检查当前设置；同时编辑仍可能覆盖。设置任务可能重新索引文档。")).TextColor(p.Muted)
				raw := x.plan.Patch
				if x.plan.Reset {
					raw = locales.Text("由服务器重置全部索引设置，保留索引和文档。")
				}
				area := ui.TextArea(c.Key(fmt.Sprintf("settings-plan/%d/%d", d.id, x.plan.Sequence)), &raw).ReadOnly(true).Label(locales.Text("将提交的完整设置变更")).Height(min(float32(270), height-290)).MinHeight(60)
				initialized := ui.Local(area, "settings-plan-origin", func() bool { return false })
				if !*initialized {
					area.SetTextSelection(0, 0)
					*initialized = true
				}
				ui.Row(c).Justify(ui.End).Gap(8).Shrink(0).Children(func() {
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview = false
						x.plan = nil
					}
					label := locales.Text("确认提交设置更改")
					if x.plan != nil && x.plan.Reset {
						label = locales.Text("确认提交设置重置")
					}
					if ui.PrimaryButton(c, label).Disabled(!s.CanPrepareSettings(d)).Clicked() {
						s.ApplySettings(d, c.Services().Invalidate)
					}
				})
			})
		})
	}
	if x.guard {
		theme.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(580), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("保留原索引设置草稿？")).Bold()
				ui.Text(c, locales.Text("离开将放弃本地草稿。已提交请求不会回滚，原服务器任务可能仍在执行。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续编辑")).Clicked() {
						x.guard = false
						x.next = nil
					}
					if ui.Button(c, locales.Text("放弃本地草稿并离开")).Disabled(d.loading).Clicked() {
						next := x.next
						x.next = nil
						x.guard = false
						x.leaveAcknowledged = true
						s.discardSettingsDraft(d)
						if next != nil {
							next(c.Services())
						}
					}
				})
			})
		})
		if !x.guard {
			x.next = nil
		}
	}
}
