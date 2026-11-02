package sqlworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func historyStateLabel(state string) string {
	switch state {
	case "success":
		return locales.Text("成功")
	case "error":
		return locales.Text("失败")
	case "cancelled":
		return locales.Text("已取消（更改可能生效）")
	case "session_lost":
		return locales.Text("会话失效（结果需核对）")
	case "partial":
		return locales.Text("部分完成")
	case "pending":
		return locales.Text("事务未提交")
	case "uncertain":
		return locales.Text("结果不确定")
	default:
		return locales.Text("全部状态")
	}
}
func (s *State) openHistorySelection(id string) {
	h := &s.history
	if !h.open || h.openEntry == nil {
		return
	}
	b := h.browsers[h.scope]
	for _, entry := range s.historyEntries(b) {
		if entry.ID == id && !entry.Summary {
			h.openEntry(entry)
			h.open = false
			return
		}
	}
}
func (s *State) historyModals(c *ui.Context) {
	h := &s.history
	s.historyRecoveryModal(c)
	if !h.open {
		h.settingsOpen, h.clearOpen = false, false
		return
	}
	b := h.browsers[h.scope]
	ui.Modal(c, &h.open, func() {
		ui.Column(c).Width(980).MaxWidthPercent(95).Height(650).MaxHeightPercent(90).Padding(16).Gap(8).Children(func() {
			ui.Text(c, locales.Text("SQL 历史与日志")).Bold()
			ui.Text(c, locales.Text("本地加密保存；打开历史只恢复文本，不执行 SQL。")).FontSize(theme.Font(c, 11))
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &b.search).Label(locales.Text("搜索历史 SQL、数据库或错误")).Grow(1)
				if ui.Button(c, locales.Text("重新读取历史")).Disabled(h.pending != nil).Clicked() {
					s.loadHistory()
				}
				if ui.Button(c, locales.Text("历史设置")).Disabled(!h.loaded || h.err != "").Clicked() {
					h.settings = s.projectedHistory().Settings
					h.settingsOpen = true
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				profiles := []string{locales.Text("全部连接")}
				seen := map[int64]bool{}
				for _, entry := range h.snapshot.Entries {
					if !seen[entry.ProfileID] {
						seen[entry.ProfileID] = true
						profiles = append(profiles, fmt.Sprintf("#%d", entry.ProfileID))
					}
				}
				if b.profile > 0 && !seen[b.profile] {
					profiles = append(profiles, fmt.Sprintf("#%d", b.profile))
				}
				profile := profiles[0]
				if b.profile != 0 {
					profile = fmt.Sprintf("#%d", b.profile)
				}
				if ui.Select(c, &profile, profiles).Label(locales.Text("历史连接筛选")).Changed() {
					b.profile, _ = strconv.ParseInt(strings.TrimPrefix(profile, "#"), 10, 64)
					b.selected, b.row, b.list = "", -1, ui.ListState{}
				}
				states := []string{"all", "success", "error", "cancelled", "session_lost", "uncertain", "partial", "pending"}
				stateLabels := make([]string, len(states))
				for i, state := range states {
					stateLabels[i] = historyStateLabel(state)
				}
				state := historyStateLabel(b.state)
				if ui.Select(c, &state, stateLabels).Label(locales.Text("历史状态筛选")).Changed() {
					for i, v := range stateLabels {
						if state == v {
							b.state = states[i]
						}
					}
				}
				historySelect(c, &b.source, "历史操作筛选", []string{"all", "query", "explain", "export", "file", "data_read", "data_write", "transaction", "import", "data_export", "backup", "restore_preview", "restore", "accounts", "structure", "table", "maintenance"}, []string{"全部操作", "执行查询", "执行计划", "执行并导出", "执行脚本文件", "读取表数据", "编辑表数据", "数据事务", "导入数据", "导出数据", "备份", "恢复预览", "恢复", "账户与权限", "结构设计", "表操作", "表维护"})
				historySelect(c, &b.age, "历史时间筛选", []string{"all", "day", "week"}, []string{"全部时间", "最近一天", "最近一周"})
			})
			if h.err != "" {
				ui.Text(c, h.err).TextColor(theme.Colors(c).Error).Tooltip(h.err)
			}
			if h.dropped > 0 {
				ui.Text(c, fmt.Sprintf(locales.Text("有 %d 条日志因大小或队列上限未记录；原文档和数据库操作不受影响。"), h.dropped)).TextColor(theme.Colors(c).Error)
			}
			if len(h.queue) > 0 && h.err != "" {
				ui.Text(c, fmt.Sprintf(locales.Text("保留 %d 条待保存日志；重新读取后重试，或明确放弃。"), len(h.queue)))
				if ui.Button(c, locales.Text("放弃待保存日志…")).Disabled(h.pending != nil).Clicked() {
					h.discardOpen = true
				}
			}
			if h.pending != nil || len(h.queue) > 0 && h.err == "" {
				ui.Text(c, locales.Text("历史记录正在保存…")).FontSize(theme.Font(c, 11))
			}
			entries := s.historyEntries(b)
			b.row = -1
			for i, entry := range entries {
				if entry.ID == b.selected {
					b.row = i
				}
			}
			b.list.Selected = &b.row
			b.list.Key = func(row int) any {
				if row < 0 || row >= len(entries) {
					return nil
				}
				return entries[row].ID
			}
			table := theme.Table(c, &b.list, []ui.TableColumn{{Title: locales.Text("时间"), Width: 145}, {Title: locales.Text("连接"), Width: 65}, {Title: locales.Text("数据库"), Width: 105}, {Title: locales.Text("结果"), Width: 140}, {Title: "SQL", Width: 420}}, len(entries), func(row, col int) {
				e := entries[row]
				text := strings.Join(strings.Fields(e.SQL), " ")
				switch col {
				case 0:
					text = e.Started.Local().Format("01-02 15:04:05")
				case 1:
					text = fmt.Sprintf("#%d", e.ProfileID)
				case 2:
					text = e.Database
				case 3:
					text = historyStateLabel(e.State)
				}
				ui.Text(c, text).SingleLine().Tooltip(text)
			}).Grow(1).MinHeight(120).Label(locales.Text("SQL 历史记录"))
			if b.row >= 0 && b.row < len(entries) {
				b.selected = entries[b.row].ID
			}
			if table.Submitted() && b.row >= 0 && b.row < len(entries) {
				s.openHistorySelection(entries[b.row].ID)
			}
			if b.row >= 0 && b.row < len(entries) {
				e := entries[b.row]
				ui.Text(c, fmt.Sprintf(locales.Text("耗时 %s · 预览 %d 行 · 影响 %d 行"), e.Duration, e.Rows, e.AffectedRows)).FontSize(theme.Font(c, 11))
				if e.Summary {
					ui.Text(c, locales.Text("操作摘要；不含参数值，不能作为 SQL 重新打开。")).FontSize(theme.Font(c, 11))
				}
				ui.Text(c, e.Source+" · "+e.Object).FontSize(theme.Font(c, 11))
				text := e.SQL
				ui.TextArea(c, &text).ReadOnly(true).Height(100).Label(locales.Text("历史 SQL 正文"))
				if e.Error != "" {
					ui.Text(c, e.Error).SingleLine().Tooltip(e.Error).TextColor(theme.Colors(c).Error)
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("清除当前范围历史…")).Disabled(!h.loaded || h.err != "").Clicked() {
					h.clearOpen = true
				}
				if ui.Button(c, locales.Text("打开历史 SQL")).Disabled(b.row < 0 || h.openEntry == nil || b.row >= len(entries) || entries[b.row].Summary).Clicked() {
					s.openHistorySelection(b.selected)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					h.open = false
				}
			})
		})
	})
	if !h.open {
		h.settingsOpen, h.clearOpen = false, false
		return
	}
	if h.clearOpen {
		if ui.AlertDialog(c, &h.clearOpen, locales.Text("清除 SQL 历史？"), locales.Text("清除所选连接范围的记录，并阻止此前执行的迟到回执重新写入。"), locales.Text("取消"), locales.Text("清除历史")) == 1 {
			s.ClearHistory(b.profile)
		}
	}
	if h.settingsOpen {
		ui.Modal(c, &h.settingsOpen, func() {
			ui.Column(c).Width(420).Padding(18).Gap(12).Children(func() {
				ui.Text(c, locales.Text("SQL 历史设置")).Bold()
				ui.Checkbox(c, &h.settings.Enabled, locales.Text("记录 SQL 执行历史"))
				days, limit := strconv.Itoa(h.settings.RetentionDays), strconv.Itoa(h.settings.MaxEntries)
				if ui.Select(c, &days, []string{"7", "30", "90", "365"}).Label(locales.Text("历史保留天数")).Changed() {
					h.settings.RetentionDays, _ = strconv.Atoi(days)
				}
				if ui.Select(c, &limit, []string{"100", "1000", "10000"}).Label(locales.Text("最多保留记录数")).Changed() {
					h.settings.MaxEntries, _ = strconv.Atoi(limit)
				}
				ui.Text(c, locales.Text("历史总正文最多 32 MiB；超过上限移除最旧完整记录。")).FontSize(theme.Font(c, 11))
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						h.settingsOpen = false
					}
					if ui.PrimaryButton(c, locales.Text("保存历史设置")).Clicked() {
						s.SaveHistorySettings(h.settings)
						h.settingsOpen = false
					}
				})
			})
		})
	}
}

// History entries are passed by value; no editor text, result or lease is
// reused when the host constructs a document in the entry's original scope.
func (s *State) SetHistoryOpener(open func(queries.HistoryEntry)) { s.history.openEntry = open }

func historySelect(c *ui.Context, value *string, label string, values, names []string) {
	labels := make([]string, len(names))
	selected := ""
	for i, name := range names {
		labels[i] = locales.Text(name)
		if values[i] == *value {
			selected = labels[i]
		}
	}
	if ui.Select(c, &selected, labels).Label(locales.Text(label)).Changed() {
		for i, text := range labels {
			if text == selected {
				*value = values[i]
			}
		}
	}
}

func (s *State) historyRecoveryModal(c *ui.Context) {
	h := &s.history
	if h.discardOpen {
		if ui.AlertDialog(c, &h.discardOpen, locales.Text("放弃待保存日志？"), locales.Text("仅放弃本次内存中尚未保存的日志和迟到回执，已保存历史及文档不变。"), locales.Text("取消"), locales.Text("放弃日志")) == 1 {
			s.DiscardPendingHistory()
		}
	}
	if h.exitOpen {
		ui.Modal(c, &h.exitOpen, func() {
			ui.Column(c).Width(520).MaxWidthPercent(95).Padding(18).Gap(12).Children(func() {
				ui.Text(c, locales.Text("日志尚未保存")).Bold()
				ui.Text(c, h.err).TextColor(theme.Colors(c).Error)
				ui.Text(c, locales.Text("重新读取后重试保存，或放弃本次待保存日志再继续退出。文档草稿仍由既有退出流程处理。"))
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("取消退出")).Clicked() {
						h.exitOpen = false
						h.exitNext = nil
					}
					if ui.Button(c, locales.Text("重试日志保存")).Disabled(h.pending != nil).Clicked() {
						s.loadHistory()
						s.persistence.awaitClose = h.exitNext
						h.exitNext = nil
						h.exitOpen = false
					}
					if ui.Button(c, locales.Text("放弃日志并继续")).Disabled(h.pending != nil).Clicked() && s.DiscardPendingHistory() {
						next := h.exitNext
						h.exitNext = nil
						h.exitOpen = false
						if next != nil {
							next(c.Services())
						}
					}
				})
			})
		})
		if !h.exitOpen {
			h.exitNext = nil
		}
	}
}
