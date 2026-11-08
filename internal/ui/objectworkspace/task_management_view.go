package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) taskManagementActions(c *ui.Context, d *Document) {
	m := &d.tasks.management
	selected := len(s.TaskSelectionRows(d)) > 0
	if d.object.Catalog == "" {
		if ui.Button(c, locales.Text("创建服务器 dump")).Disabled(!s.CanCreateServerBackup(d, "dump")).Clicked() {
			_ = s.PrepareServerBackup(d, "dump")
		}
		if ui.Button(c, locales.Text("创建服务器 snapshot")).Disabled(!s.CanCreateServerBackup(d, "snapshot")).Clicked() {
			_ = s.PrepareServerBackup(d, "snapshot")
		}
	}
	if ui.Button(c, locales.Text("预览取消选中任务")).Disabled(!selected || !s.CanManageTasks(d)).Clicked() {
		_ = s.PrepareTaskManagement(d, "cancel", nil)
	}
	if ui.Button(c, locales.Text("预览删除选中任务记录")).Disabled(!selected || !s.CanManageTasks(d)).Clicked() {
		_ = s.PrepareTaskManagement(d, "delete", nil)
	}
	if ui.Button(c, locales.Text("观察原管理任务（30 秒）")).Disabled(!s.taskReady(d) || m.receipt.Raw == "").Clicked() {
		s.ObserveTaskManagement(d, c.Services().Invalidate)
	}
	if ui.Button(c, locales.Text("查看管理任务回执和结果")).Disabled(m.last.Request == "" && m.last.Receipt.Raw == "" && m.last.TaskRaw == "" && !m.uncertain).Clicked() {
		d.tasks.tab = 3
	}
}
func (s *State) taskManagementEvidence(c *ui.Context, d *Document) {
	m := &d.tasks.management
	p := theme.Colors(c)
	ui.Text(c, locales.Text("入队回执与完成结果分开确认；本地停止等待不会取消服务器任务。")).SingleLine().Tooltip(locales.Text("入队回执与完成结果分开确认；本地停止等待不会取消服务器任务。")).TextColor(p.Muted).Padding(2, 8).Shrink(0)
	state := taskStatusLabel(m.last.State)
	if m.uncertain {
		state = locales.Text("结果未知")
	}
	if m.last.State == "rejected" {
		state = locales.Text("请求被拒绝")
	}
	if m.last.State == "not_submitted" {
		state = locales.Text("未提交")
	}
	if state == "" {
		state = "—"
	}
	ui.Text(c, state).Padding(2, 8).Shrink(0)
	if m.last.CountsKnown {
		ui.Text(c, fmt.Sprintf(locales.Text("原管理任务匹配 %d 条，实际处理 %d 条"), m.last.Matched, m.last.Changed)).Padding(2, 8).Shrink(0)
	}
	if m.last.Artifact.Kind != "" {
		ui.Text(c, locales.Text("服务器备份任务已完成；文件仍需在服务器目录确认。HTTP 不提供下载，恢复须离线启动新实例。")).SingleLine().Tooltip(locales.Text("服务器备份任务已完成；文件仍需在服务器目录确认。HTTP 不提供下载，恢复须离线启动新实例。")).Padding(2, 8).Shrink(0)
		if m.last.Artifact.UID != "" {
			ui.Text(c, "dumpUid: "+m.last.Artifact.UID).SingleLine().Tooltip(m.last.Artifact.UID).Padding(2, 8).Shrink(0)
		}
	}
	raw := m.last.Request
	if m.last.Receipt.Raw != "" {
		raw += "\n\n" + m.last.Receipt.Raw
	}
	if m.last.TaskRaw != "" {
		raw += "\n\n" + m.last.TaskRaw
	}
	if raw == "" {
		ui.Text(c, locales.Text("尚无原管理任务回执；未知提交请检查原服务器任务。")).Padding(8)
		ui.Box(c).Grow(1)
	} else {
		preview, truncated := taskPreview(raw)
		if truncated {
			ui.Text(c, locales.Text("任务 JSON 预览已截断；可复制完整内容。")).Padding(2, 8).TextColor(p.Muted).Shrink(0)
		}
		area := ui.TextArea(c.Key(fmt.Sprintf("task-management-evidence/%d", d.id)), &preview).ReadOnly(true).Label(locales.Text("原管理任务完整证据预览")).Fill().MinHeight(0)
		seen := ui.Local(area, "original-evidence", func() string { return "" })
		if *seen != preview {
			area.SetTextSelection(0, 0)
			*seen = preview
		}
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制原管理请求")).Disabled(m.last.Request == "").Clicked() {
			c.WriteClipboard(m.last.Request)
		}
		if ui.Button(c, locales.Text("复制原管理入队回执")).Disabled(m.last.Receipt.Raw == "").Clicked() {
			c.WriteClipboard(m.last.Receipt.Raw)
		}
		if ui.Button(c, locales.Text("复制原管理任务结果")).Disabled(m.last.TaskRaw == "").Clicked() {
			c.WriteClipboard(m.last.TaskRaw)
		}
	}).FillWidth().Shrink(0).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 8).Gap(4)
}
func (s *State) taskManagementModals(c *ui.Context, d *Document) {
	m := &d.tasks.management
	if m.preview && m.plan != nil {
		theme.Modal(c, &m.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				title, label := "取消原任务预览", "确认提交任务取消"
				if m.plan.Action == "delete" {
					title, label = "删除原任务记录预览", "确认提交任务记录删除"
				}
				backup := serverBackupAction(m.plan.Action)
				if m.plan.Action == "dump" {
					title, label = "创建服务器 dump 预览", "确认创建服务器 dump"
				}
				if m.plan.Action == "snapshot" {
					title, label = "创建服务器 snapshot 预览", "确认创建服务器 snapshot"
				}
				ui.Text(c, locales.Text(title)).Bold()
				profile := d.object.Catalog
				if s.deps.ProfileName != nil {
					profile = s.deps.ProfileName(d.profileID) + " · " + profile
				}
				ui.Text(c, profile).SingleLine().Tooltip(d.object.Catalog)
				ids := []string{}
				for _, r := range m.plan.Records {
					ids = append(ids, r.UID)
				}
				if backup {
					ui.Text(c, locales.Text("范围：原连接服务器的全部索引、设置与密钥")).Shrink(0)
					location := "文件写入服务器配置的 dump/snapshot 目录；入队不代表完成，HTTP 不提供文件下载。"
					if m.plan.Action == "snapshot" {
						location = "snapshot 会替换服务器现有快照文件；保留旧文件须先归档。文件留在服务器配置的目录，HTTP 不提供下载。"
					}
					ui.Text(c, locales.Text(location)).TextColor(theme.Colors(c).Muted)
					ui.Text(c, locales.Text("dump 用 --import-dump，snapshot 用 --import-snapshot，在兼容版本的新空数据目录离线恢复；备份文件可能包含密钥。")).TextColor(theme.Colors(c).Muted)
				} else {
					ui.Text(c, fmt.Sprintf(locales.Text("仅操作以下 %d 个原任务 UID"), len(ids))).Shrink(0)
					ui.Text(c, locales.Text("取消只针对未完成任务；删除仅移除已结束的任务记录，保留文档。")).TextColor(theme.Colors(c).Muted)
					ui.Text(c, locales.Text("提交前检查原任务；状态可能继续变化。原生 UID 无法原子防护服务器重启或恢复。")).TextColor(theme.Colors(c).Muted)
				}
				raw := strings.Join(ids, ", ") + "\n\n" + m.plan.Request
				ui.TextArea(c.Key(fmt.Sprintf("task-management-plan/%d/%d", d.id, m.plan.Sequence)), &raw).ReadOnly(true).Label(locales.Text("完整任务管理预览")).Height(min(float32(250), height-310)).MinHeight(40)
				ui.Row(c).Justify(ui.End).Shrink(0).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("返回")).Clicked() {
						m.preview = false
						m.plan = nil
					}
					if ui.PrimaryButton(c, locales.Text(label)).Disabled(!s.canSubmitTaskPlan(d)).Clicked() {
						s.SubmitTaskManagement(d, c.Services().Invalidate)
					}
				})
			})
		})
	}
	if m.guard {
		theme.Modal(c, &m.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(580), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("离开原任务管理页面？")).Bold()
				ui.Text(c, locales.Text("关闭页面会丢弃本地管理证据；已提交的服务器任务不会回滚。请先确认原任务结果。")).TextColor(theme.Colors(c).Muted)
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续确认原任务")).Clicked() {
						m.guard = false
						m.next = nil
					}
					if ui.Button(c, locales.Text("放弃本地证据并离开")).Disabled(d.loading).Clicked() {
						next := m.next
						m.guard = false
						m.next = nil
						m.preview = false
						m.plan = nil
						m.acknowledged = true
						if next != nil {
							next(c.Services())
						}
					}
				})
			})
		})
		if !m.guard {
			m.next = nil
		}
	}
}
