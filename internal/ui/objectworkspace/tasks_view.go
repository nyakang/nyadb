package objectworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var taskColumns = []ui.TableColumn{
	{ID: "uid", Title: "任务 UID", Width: 78}, {ID: "index", Title: "索引", Width: 128}, {ID: "type", Title: "任务类型", Width: 200}, {ID: "status", Title: "任务状态", Width: 98},
	{ID: "enqueued", Title: "入队时间", Width: 220}, {ID: "started", Title: "开始时间", Width: 220}, {ID: "finished", Title: "完成时间", Width: 220}, {ID: "duration", Title: "原生耗时", Width: 140}, {ID: "details", Title: "任务详情", Width: 280},
}

func taskStatusLabel(status string) string {
	switch status {
	case "enqueued":
		return locales.Text("已入队")
	case "processing":
		return locales.Text("执行中")
	case "succeeded":
		return locales.Text("成功")
	case "failed":
		return locales.Text("失败")
	case "canceled":
		return locales.Text("已取消")
	}
	return status
}
func taskCell(record contract.TaskRecord, column int) string {
	switch column {
	case 0:
		return record.UID
	case 1:
		if record.Index == "" {
			return "—"
		}
		return record.Index
	case 2:
		return record.Type
	case 3:
		return taskStatusLabel(record.Status)
	case 4:
		return record.EnqueuedAt
	case 5:
		return record.StartedAt
	case 6:
		return record.FinishedAt
	case 7:
		return record.Duration
	case 8:
		return record.Details
	}
	return ""
}
func taskPreview(raw string) (string, bool) {
	n := 0
	for i := range raw {
		if n == 20480 {
			return raw[:i], true
		}
		n++
	}
	return raw, false
}
func (s *State) taskInput(c *ui.Context, d *Document, value *string, label string) ui.Element {
	e := ui.TextInput(c, value).Label(locales.Text(label)).ReadOnly(d.loading)
	return theme.BindNativeEdit(c, e, fmt.Sprintf("tasks/%d/%p", d.id, value), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !d.closed && d.tasks != nil }, !d.loading, false)
}
func (s *State) taskField(c *ui.Context, d *Document, value *string, label string) {
	theme.Field(c, locales.Text(label), func() { s.taskInput(c, d, value, label).FillWidth() }).Grow(1).MinWidth(0)
}
func (s *State) tasksView(c *ui.Context, d *Document) {
	x := d.tasks
	p := theme.Colors(c)
	ready := s.taskReady(d)
	scope := d.object.Catalog
	if scope == "" {
		scope = locales.Text("全部索引")
	}
	ui.Column(c).Fill().Background(p.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Shrink(0).Children(func() {
			ui.Text(c, "Meilisearch · "+locales.Text("任务")+" · "+scope).SingleLine().Grow(1).MinWidth(0).Tooltip("Meilisearch · " + scope)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("刷新任务")).Disabled(!ready).Clicked() {
					s.LoadTasks(d, c.Services().Invalidate, true)
				}
				if ui.Button(c, locales.Text("重置任务筛选")).Disabled(!ready).Clicked() {
					s.ResetTaskFilters(d, c.Services().Invalidate)
				}
				if ui.Button(c, locales.Text("高级任务筛选")).Clicked() {
					x.filtersOpen = !x.filtersOpen
				}
				if ui.Button(c, locales.Text("恢复任务连接")).Disabled(d.loading || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected).Clicked() {
					s.RecoverTasks(d)
				}
				if ui.Button(c, locales.Text("复制完整任务响应")).Disabled(x.page == nil && x.failureRaw == "").Clicked() {
					raw := x.failureRaw
					if raw == "" && x.page != nil {
						raw = x.page.Raw
					}
					c.WriteClipboard(raw)
				}
				if d.loading && ui.Button(c, locales.Text("停止任务观察")).Disabled(x.cancelRequested).Clicked() {
					s.CancelTasks(d)
				}
				if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
					s.deps.Back()
				}
			}).Label(locales.Text("任务操作")).Grow(1).MinWidth(0).Gap(4)
		})
		ui.Column(c).FillWidth().Padding(4, 8).Gap(4).Background(p.Panel).Shrink(0).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.End).Children(func() {
				s.taskField(c, d, &x.uids, "任务 UID 筛选")
				theme.Field(c, locales.Text("任务状态"), func() {
					labels := []string{locales.Text("全部状态"), taskStatusLabel("enqueued"), taskStatusLabel("processing"), taskStatusLabel("succeeded"), taskStatusLabel("failed"), taskStatusLabel("canceled")}
					values := []string{"全部状态", "enqueued", "processing", "succeeded", "failed", "canceled"}
					current := labels[0]
					for i, v := range values {
						if v == x.status {
							current = labels[i]
						}
					}
					ui.Select(c, &current, labels).Label(locales.Text("任务状态筛选")).Disabled(d.loading).FillWidth()
					for i, v := range labels {
						if v == current {
							x.status = values[i]
						}
					}
				}).Width(112)
				if ui.PrimaryButton(c, locales.Text("应用任务筛选")).Disabled(!ready).Clicked() {
					s.ApplyTaskFilters(d, c.Services().Invalidate)
				}
			})
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.End).Children(func() {
				theme.Field(c, locales.Text("任务顺序"), func() {
					labels := []string{locales.Text("最新任务优先"), locales.Text("最早任务优先")}
					current := labels[0]
					if x.order == "最早任务优先" {
						current = labels[1]
					}
					ui.Select(c, &current, labels).Label(locales.Text("任务顺序")).Disabled(d.loading).FillWidth()
					x.order = "最新任务优先"
					if current == labels[1] {
						x.order = "最早任务优先"
					}
				}).Grow(1).MinWidth(0)
				theme.Field(c, locales.Text("任务页大小"), func() { s.taskInput(c, d, &x.limit, "任务页大小").FillWidth() }).Width(72)
				ui.Text(c, locales.Text("任务分页为实时读取；刷新可能改变任务状态与总数。")).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(p.Muted).SingleLine().Grow(2).MinWidth(0).Tooltip(locales.Text("任务分页为实时读取；刷新可能改变任务状态与总数。"))
			})
		})
		if x.filtersOpen {
			ui.Scroll(c).Height(0).Grow(1).MinHeight(0).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Padding(4, 8).Gap(6).Children(func() {
					ui.Row(c).FillWidth().Gap(6).Children(func() {
						if d.object.Catalog == "" {
							s.taskField(c, d, &x.indexes, "索引 UID 筛选")
						}
						s.taskField(c, d, &x.kind, "任务类型筛选")
					})
					ui.Row(c).FillWidth().Gap(6).Children(func() {
						s.taskField(c, d, &x.batch, "Batch UID 筛选")
						s.taskField(c, d, &x.canceledBy, "取消任务 UID 筛选")
					})
					for _, entry := range []struct {
						before, after           *string
						beforeLabel, afterLabel string
					}{{&x.beforeEnqueued, &x.afterEnqueued, "入队时间早于", "入队时间晚于"}, {&x.beforeStarted, &x.afterStarted, "开始时间早于", "开始时间晚于"}, {&x.beforeFinished, &x.afterFinished, "完成时间早于", "完成时间晚于"}} {
						entry := entry
						ui.Row(c).FillWidth().Gap(6).Children(func() {
							s.taskField(c, d, entry.before, entry.beforeLabel)
							s.taskField(c, d, entry.after, entry.afterLabel)
						})
					}
					ui.Text(c, locales.Text("多个值用逗号分隔；时间使用带时区的 RFC3339 格式。")).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(p.Muted)
				})
			})
		}
		if d.loading {
			message := locales.Text("正在读取原连接任务…")
			if x.cancelRequested {
				message = locales.Text("正在等待原任务请求停止…")
			}
			ui.Text(c, message).Padding(2, 8).SingleLine().TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, locales.Text(d.err)).Padding(2, 8).SingleLine().TextColor(p.Error).Tooltip(locales.Text(d.err))
		}
		if !x.filtersOpen {
			ui.Tabs(c, &x.tab, locales.Text("任务列表"), locales.Text("任务详情"), locales.Text("任务响应"), locales.Text("管理回执与结果")).FillWidth()
			if x.tab == 0 {
				rows := 0
				if x.page != nil {
					rows = len(x.page.Results)
				}
				if rows == 0 {
					message := locales.Text("没有已加载的任务")
					if x.page != nil {
						message = locales.Text("当前筛选下没有任务")
					}
					theme.EmptyTable(c, &d.columns, taskColumns, message)
				} else {
					d.columns.Selected = &x.selected
					d.columns.Selection = &x.selection
					d.columns.Key = func(row int) any {
						if row < 0 || row >= len(x.page.Results) {
							return nil
						}
						return taskSelectionKey(x.page.Results[row])
					}
					old := x.selected
					theme.Table(c, &d.columns, taskColumns, rows, func(row, col int) {
						text := taskCell(x.page.Results[row], col)
						ui.Text(c, text).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Tooltip(text)
					}).Grow(1).MinHeight(0).Label(locales.Text("原连接任务表格"))
					if old != x.selected {
						s.SelectTask(d, x.selected)
					}
				}
			} else if x.tab == 3 {
				s.taskManagementEvidence(c, d)
			} else {
				raw, label := "", locales.Text("完整原生任务详情预览")
				if x.tab == 2 {
					label = locales.Text("原生任务响应预览")
					raw = x.failureRaw
					if raw == "" && x.page != nil {
						raw = x.page.Raw
					}
				} else if x.detail != nil {
					raw = x.detail.Raw
				}
				if raw == "" {
					ui.Text(c, locales.Text("请选择任务查看原生详情")).Padding(8)
					ui.Box(c).Grow(1)
				} else {
					preview, truncated := taskPreview(raw)
					if truncated {
						ui.Text(c, locales.Text("任务 JSON 预览已截断；可复制完整内容。")).Padding(2, 8).TextColor(p.Muted)
					}
					area := ui.TextArea(c.Key(fmt.Sprintf("tasks-preview/%d/%d", d.id, x.tab)), &preview).ReadOnly(true).Label(label).Fill().MinHeight(0)
					if !x.previewVisible || x.previewTab != x.tab || x.previewRaw != raw {
						area.SetTextSelection(0, 0)
					}
					x.previewVisible, x.previewTab, x.previewRaw = true, x.tab, raw
				}
			}
			if x.tab == 0 {
				x.previewVisible = false
			}
			selected := x.page != nil && x.selected >= 0 && x.selected < len(x.page.Results)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("查看任务详情")).Disabled(!selected).Clicked() {
					s.SelectTask(d, x.selected)
					x.tab = 1
				}
				if ui.Button(c, locales.Text("刷新选中任务")).Disabled(!ready || !selected || !x.fresh).Clicked() {
					s.ReadTask(d, c.Services().Invalidate, false)
				}
				if ui.Button(c, locales.Text("观察选中任务（30 秒）")).Disabled(!ready || !selected || !x.fresh).Clicked() {
					s.ReadTask(d, c.Services().Invalidate, true)
				}
				if ui.Button(c, locales.Text("复制完整任务详情")).Disabled(x.detail == nil).Clicked() {
					c.WriteClipboard(x.detail.Raw)
				}
				s.taskManagementActions(c, d)
			}).Label(locales.Text("选中任务操作")).Shrink(0).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 8).Gap(4)
		} else {
			x.previewVisible = false
		}
		ui.Row(c).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Shrink(0).Children(func() {
			total := "—"
			if x.page != nil {
				total = strconv.FormatUint(x.page.Total, 10)
			}
			ui.Text(c, fmt.Sprintf(locales.Text("任务总数：%s · 第 %d 页"), total, len(x.cursors))).SingleLine().Grow(1).MinWidth(0)
			if ui.Button(c, locales.Text("上一页")).Label(locales.Text("上一页任务")).Disabled(!ready || !x.fresh || len(x.cursors) < 2).Clicked() {
				s.TaskPrevious(d, c.Services().Invalidate)
			}
			if ui.Button(c, locales.Text("下一页")).Label(locales.Text("下一页任务")).Disabled(!ready || !x.fresh || x.page == nil || x.page.Next == "").Clicked() {
				s.TaskNext(d, c.Services().Invalidate)
			}
		})
	})
	s.taskManagementModals(c, d)
}
