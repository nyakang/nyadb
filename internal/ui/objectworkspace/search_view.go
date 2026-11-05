package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) searchModal(c *ui.Context) {
	x := s.search
	if x == nil || !x.open {
		return
	}
	theme.Modal(c, &x.open, func() {
		width, height := c.Size()
		ui.Column(c).Width(min(float32(1000), width-68)).Height(min(float32(680), height-68)).Gap(8).Children(func() {
			ui.Text(c, locales.Text("在数据库或模式中查找")).Bold()
			ui.Text(c, s.searchProfileLabel(x.profile)+" · "+locales.Text("只读元数据搜索，不读取表记录。")).FontSize(theme.Font(c, 11))
			ui.Row(c).Gap(8).Children(func() {
				if ui.SearchField(c, &x.text).Label(locales.Text("查找对象名称、注释或定义")).Placeholder(locales.Text("查找对象名称、注释或定义")).Disabled(x.busy).Grow(1).Changed() {
					s.clearObjectSearchEntries()
				}
				if ui.PrimaryButton(c, locales.Text("开始查找")).Disabled(x.busy || strings.TrimSpace(x.text) == "").Clicked() {
					s.startObjectSearch(c.Services())
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				options := []string{locales.Text("全部数据库或模式")}
				for _, namespace := range x.namespaces {
					options = append(options, namespace.Label)
				}
				scope := x.scope
				if scope == "" {
					scope = options[0]
				}
				if ui.Select(c, &scope, options).Label(locales.Text("查找范围")).Disabled(x.busy).Changed() {
					s.clearObjectSearchEntries()
					x.scope = scope
					if scope == options[0] {
						x.scope = ""
					}
				}
				kind := locales.Text(x.kind)
				kinds := []string{"全部", "表", "视图", "函数", "存储过程", "事件", "序列"}
				labels := make([]string, len(kinds))
				for i, k := range kinds {
					labels[i] = locales.Text(k)
				}
				if ui.Select(c, &kind, labels).Label(locales.Text("查找对象类型")).Disabled(x.busy).Changed() {
					for i, k := range labels {
						if k == kind {
							s.clearObjectSearchEntries()
							x.kind = kinds[i]
						}
					}
				}
				modeNames := []string{locales.Text("包含"), locales.Text("名称前缀"), locales.Text("完全匹配")}
				modes := []string{"contains", "prefix", "exact"}
				mode := modeNames[0]
				for i, m := range modes {
					if m == x.mode {
						mode = modeNames[i]
					}
				}
				if ui.Select(c, &mode, modeNames).Label(locales.Text("查找匹配方式")).Disabled(x.busy).Changed() {
					for i, m := range modeNames {
						if m == mode {
							s.clearObjectSearchEntries()
							x.mode = modes[i]
						}
					}
				}
			})
			ui.Row(c).Gap(12).Children(func() {
				if ui.Checkbox(c, &x.fields, locales.Text("查找字段名称和注释")).Disabled(x.busy || !x.canFields).Changed() {
					s.clearObjectSearchEntries()
				}
				if ui.Checkbox(c, &x.definitions, locales.Text("查找程序和视图定义")).Disabled(x.busy).Changed() {
					s.clearObjectSearchEntries()
				}
				if ui.Checkbox(c, &x.caseSensitive, locales.Text("区分大小写")).Disabled(x.busy).Changed() {
					s.clearObjectSearchEntries()
				}
			})
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			ui.Text(c, fmt.Sprintf(locales.Text("找到 %d 项"), len(x.entries))).FontSize(theme.Font(c, 11))
			x.list.Selected = &x.row
			table := theme.Table(c, &x.list, []ui.TableColumn{{Title: locales.Text("数据库或模式"), Width: 150}, {Title: locales.Text("类型"), Width: 90}, {Title: locales.Text("名称"), Width: 180}, {Title: locales.Text("匹配位置"), Width: 80}, {Title: locales.Text("匹配内容"), Width: 400}}, len(x.entries), func(row, column int) {
				entry := x.entries[row]
				value := entry.Match
				switch column {
				case 0:
					value = entry.Namespace.Label
				case 1:
					value = locales.Text(entry.Object.Kind)
				case 2:
					value = entry.Object.Name
				case 3:
					value = locales.Text(entry.Field)
				}
				ui.Text(c, value).SingleLine().Tooltip(value)
			}).Grow(1).MinHeight(100).Label(locales.Text("对象查找结果"))
			if table.Submitted() {
				s.openSearchEntry(c.Services(), x.row)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					x.open = false
					s.cancelObjectSearch()
				}
				if ui.Button(c, locales.Text("取消查找")).Disabled(!x.busy).Clicked() {
					s.cancelObjectSearch()
				}
				if ui.Button(c, locales.Text("打开所选对象")).Disabled(x.busy || x.row < 0).Clicked() {
					s.openSearchEntry(c.Services(), x.row)
				}
			})
		})
	})
	if !x.open {
		s.cancelObjectSearch()
	}
}

func (s *State) searchProfileLabel(id int64) string {
	if s.deps.ProfileName != nil {
		return fmt.Sprintf(locales.Text("%s（#%d）"), s.deps.ProfileName(id), id)
	}
	return fmt.Sprintf(locales.Text("连接 #%d"), id)
}
