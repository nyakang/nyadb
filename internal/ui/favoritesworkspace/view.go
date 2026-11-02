package favoritesworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) View(c *ui.Context) {
	if !s.open {
		return
	}
	ui.Modal(c, &s.open, func() {
		ui.Column(c).Width(960).MaxWidthPercent(95).Height(620).MaxHeightPercent(92).Padding(16).Gap(8).Children(func() {
			ui.Text(c, locales.Text("收藏夹管理")).Bold()
			ui.Text(c, locales.Text("连接星标、对象和命名查询引用；打开查询仅恢复文本。"))
			ui.Row(c).Gap(8).Children(func() {
				if ui.TextInput(c, &s.search).Label(locales.Text("搜索收藏")).Grow(1).Changed() {
					s.row = -1
					s.selected = ""
					s.list = ui.ListState{}
				}
				options := []string{locales.Text("全部收藏分组"), locales.Text("未分组")}
				ids := []string{"", "ungrouped"}
				for _, g := range s.snapshot.Groups {
					options = append(options, g.Name)
					ids = append(ids, g.ID)
				}
				counts := map[string]int{}
				for _, option := range options {
					counts[option]++
				}
				for i, option := range options {
					if counts[option] > 1 {
						suffix := ids[i]
						if suffix == "" {
							suffix = "all"
						}
						options[i] = option + " [" + suffix + "]"
					}
				}
				label := options[0]
				for i, id := range ids {
					if id == s.group {
						label = options[i]
					}
				}
				if ui.Select(c, &label, options).Label(locales.Text("收藏分组")).Changed() {
					for i, l := range options {
						if l == label {
							s.group = ids[i]
						}
					}
					s.row = -1
					s.selected = ""
					s.list = ui.ListState{}
				}
				if ui.Button(c, locales.Text("刷新收藏")).Disabled(s.busy).Clicked() {
					s.Refresh()
				}
			})
			if s.err != "" {
				ui.Text(c, locales.Text(s.err)).SingleLine().Tooltip(locales.Text(s.err)).TextColor(theme.Colors(c).Error)
			}
			items := s.items()
			s.row = -1
			for i, item := range items {
				if item.Entry.ID == s.selected {
					s.row = i
				}
			}
			s.list.Selected = &s.row
			s.list.Key = func(i int) any {
				if i < 0 || i >= len(items) {
					return nil
				}
				return items[i].Entry.ID
			}
			s.list.CellMenu = func(m *ui.Menu, row, col int) {
				if row >= 0 && row < len(items) {
					s.selected = items[row].Entry.ID
					s.contextMenu(c.Services(), m, items[row])
				}
			}
			table := theme.Table(c, &s.list, []ui.TableColumn{{Title: locales.Text("名称"), Width: 220}, {Title: locales.Text("连接"), Width: 170}, {Title: locales.Text("数据库或模式"), Width: 160}, {Title: locales.Text("类型"), Width: 90}, {Title: locales.Text("分组"), Width: 150}}, len(items), func(row, column int) {
				item := items[row]
				value := item.Entry.Name
				switch column {
				case 1:
					value = item.Profile
				case 2:
					value = item.Database
				case 3:
					value = locales.Text(item.Type)
					if !item.Available {
						reason := item.Reason
						if reason == "" {
							reason = "已失效"
						}
						value += " · " + locales.Text(reason)
					}
				case 4:
					value = locales.Text("未分组")
					for _, g := range s.snapshot.Groups {
						if g.ID == item.Entry.GroupID {
							value = g.Name
						}
					}
				}
				ui.Text(c, value).SingleLine().Tooltip(value)
			}).Grow(1).MinHeight(80).Label(locales.Text("收藏列表"))
			if table.Changed() && s.row >= 0 && s.row < len(items) {
				s.selected = items[s.row].Entry.ID
			}
			if table.Submitted() && s.row >= 0 && s.row < len(items) {
				s.openItem(c.Services(), items[s.row])
			}
			if table.Focused() && s.dialog == nil {
				if c.Shortcut(0, ui.KeyF2) {
					s.begin("rename", s.selected)
				}
				if c.Shortcut(0, ui.KeyDelete) {
					s.begin("delete", s.selected)
				}
			}
			ui.Text(c, fmt.Sprintf(locales.Text("收藏共 %d 项"), len(items))).FontSize(theme.Font(c, 11))
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("新建收藏分组")).Disabled(s.busy || !s.loaded).Clicked() {
					s.begin("new_group", "")
				}
				grouped := s.group != "" && s.group != "ungrouped"
				if ui.Button(c, locales.Text("重命名收藏分组")).Disabled(s.busy || !grouped).Clicked() {
					s.begin("rename_group", s.group)
				}
				if ui.Button(c, locales.Text("删除收藏分组…")).Disabled(s.busy || !grouped).Clicked() {
					s.begin("delete_group", s.group)
				}
				ui.Box(c).Grow(1)
				item, selected := s.selectedItem()
				if ui.Button(c, locales.Text("打开收藏")).Disabled(s.busy || !selected || !item.Available).Clicked() {
					s.openItem(c.Services(), item)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					s.Close()
				}
			})
		})
	})
	if !s.open {
		s.Close()
		return
	}
	s.dialogView(c)
}
func (s *State) dialogView(c *ui.Context) {
	d := s.dialog
	if d == nil {
		return
	}
	open := true
	titles := map[string]string{"add": "添加到收藏夹", "rename": "重命名收藏", "delete": "移除收藏？", "new_group": "新建收藏分组", "rename_group": "重命名收藏分组", "delete_group": "删除收藏分组？"}
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(460).MaxWidthPercent(92).Padding(16).Gap(12).Children(func() {
			ui.Text(c, locales.Text(titles[d.kind])).Bold()
			deleting := strings.HasPrefix(d.kind, "delete")
			if deleting {
				ui.Text(c, d.name).SingleLine()
				ui.Text(c, locales.Text("仅移除收藏引用；删除分组后条目转为未分组。"))
			} else {
				ui.TextInput(c, &d.name).Label(locales.Text("收藏名称")).Disabled(s.busy).FillWidth()
			}
			if s.err != "" {
				ui.Text(c, locales.Text(s.err)).SingleLine().Tooltip(locales.Text(s.err)).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					s.dialog = nil
				}
				if ui.PrimaryButton(c, locales.Text("确认收藏操作")).Disabled(s.busy || !deleting && strings.TrimSpace(d.name) == "").Clicked() {
					s.apply()
				}
			})
		})
	})
	if !open {
		s.dialog = nil
	}
}
