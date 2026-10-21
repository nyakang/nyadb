package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) workspace(c *ui.Context) {
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		ui.Row(c).Height(32).AlignItems(ui.Center).Padding(0, 6).
			Background(theme.Header).Gap(6).Children(func() {
			ui.ScrollHorizontal(c).Grow(1).Children(func() {
				tabs := ui.TabsBase(c, &s.activeTab, len(s.tabs))
				tabs.List.Gap(3).Children(func() {
					for i, doc := range s.tabs {
						index, current := i, doc
						item := tabs.Tab(i).Height(28).Padding(3, 8).Radius(3)
						if i == s.activeTab {
							item.Background(theme.Panel)
						}
						item.Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
								icon := "database"
								if current.kind == tabSQL {
									icon = "query"
								} else if current.kind == tabData {
									icon = "table"
								} else if current.kind == tabStructure {
									icon = "table"
								}
								theme.Icon(c, icon, 14, theme.Blue)
								ui.Text(c, current.title).FontSize(12).SingleLine()
								if current.kind != tabObjects {
									if ui.Button(c, "×").Label("关闭 "+current.title).
										Padding(0, 4).Clicked() {
										s.closeTab(index)
									}
								}
							})
						})
						item.ContextMenu(func(m *ui.Menu) {
							if m.Item("关闭 " + current.title).Disabled(index == 0).Chosen() {
								s.closeTab(index)
							}
							if m.Item("关闭其他页签").Disabled(len(s.tabs) <= 1 || (len(s.tabs) == 2 && index != 0)).Chosen() {
								s.closeOtherTabs(index)
							}
						})
					}
				})
			})
			if ui.Button(c, "＋").Label("新建 SQL 页签").Padding(3, 8).Clicked() {
				s.openQuery()
			}
		})
		switch tab := s.currentTab(); tab.kind {
		case tabSQL:
			s.queryWorkspace(c, tab)
		case tabData:
			s.dataPreview(c)
		case tabStructure:
			s.mysqlStructureWorkspace(c, tab)
		default:
			s.objectsWorkspace(c)
		}
	})
}

func (s *State) objectActions(c *ui.Context) {
	ui.Row(c).Height(31).AlignItems(ui.Center).Gap(6).Padding(2, 6).
		Background(theme.Header).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		ui.Toolbar(c, func() {
			for _, name := range []string{"打开表", "设计表", "新建表", "删除表", "导入向导", "导出向导"} {
				action := name
				if ui.Button(c, action).Padding(4, 6).Border(1, theme.Border).Radius(3).FontSize(12).Clicked() {
					if action == "打开表" {
						items := s.visibleObjects()
						if s.objectRow >= 0 && s.objectRow < len(items) && items[s.objectRow].kind == "表" {
							s.openData(items[s.objectRow].name)
						} else {
							s.status = "请先选择一个模拟数据表"
						}
					} else {
						s.status = action + "：将在后续里程碑接入"
					}
				}
			}
		}).Label("对象操作").Grow(1).MinWidth(0).Padding(0).Gap(6).Background(theme.Header)
		if ui.Button(c, "刷新").Padding(3, 6).Border(1, theme.Border).Radius(3).Clicked() {
			s.status = "已刷新模拟对象列表"
		}
	})
}

func (s *State) objectsWorkspace(c *ui.Context) {
	if s.profileMode {
		s.mysqlDatabaseWorkspace(c)
		return
	}
	ui.Column(c).Fill().Children(func() {
		s.objectActions(c)
		items := s.visibleObjects()
		if len(items) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				theme.Icon(c, "database", 40, theme.Muted)
				ui.Text(c, "选择左侧连接或数据库以浏览对象").FontSize(14).TextColor(theme.Muted)
				ui.Text(c, "当前为 UI 预览，数据库连接将在 M2 接入").FontSize(12).TextColor(theme.Muted)
			})
			return
		}
		s.objectState.Selected = &s.objectRow
		if ui.Table(c, &s.objectState, objectColumns, len(items), func(row, col int) {
			obj := items[row]
			var value string
			switch col {
			case 0:
				value = obj.name
			case 1:
				value = obj.modified
			case 2:
				value = "—"
			case 3:
				value = obj.engine
			case 4:
				value = obj.rows
			case 5:
				value = obj.comment
			}
			if col == 0 {
				ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
					symbol := "table"
					if obj.kind == "视图" {
						symbol = "view"
					}
					if obj.kind == "函数" {
						symbol = "function"
					}
					theme.Icon(c, symbol, 14, theme.Blue)
					ui.Text(c, value).SingleLine()
				})
			} else {
				ui.Text(c, value).SingleLine()
			}
		}).Grow(1).Submitted() {
			if s.objectRow >= 0 && s.objectRow < len(items) && items[s.objectRow].kind == "表" {
				s.openData(items[s.objectRow].name)
			}
		}
	})
}

func (s *State) queryWorkspace(c *ui.Context, tab *tab) {
	ui.SplitVerticalPercent(c, &s.editorPercent,
		func() {
			ui.Column(c).Fill().Children(func() {
				ui.Row(c).Height(36).Padding(4, 10).Gap(10).AlignItems(ui.Center).
					Background(theme.Header).Children(func() {
					ui.Text(c, "query.sql").TextColor(theme.Muted).FontSize(12)
					ui.Box(c).Grow(1)
					if ui.PrimaryButton(c, "运行模拟查询").Clicked() {
						s.demoRuns++
						s.status = fmt.Sprintf("模拟查询已运行 %d 次（没有访问数据库）", s.demoRuns)
					}
					if ui.Button(c, "取消").Clicked() {
						s.status = "没有运行中的真实查询"
					}
				})
				editor.View(c, tab.editor).Grow(1).FillWidth()
			})
		},
		func() { s.dataPreview(c) },
	).Grow(1)
}

func (s *State) dataPreview(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(34).AlignItems(ui.Center).Padding(4, 10).Gap(12).
			Background(theme.Header).Children(func() {
			ui.Text(c, "数据预览").Bold().FontSize(12)
			ui.Text(c, "模拟数据 · 100,000 行 / 50 列").TextColor(theme.Muted).FontSize(12)
		})
		ui.Table(c, &s.dataState, s.dataColumns, PreviewRows, func(row, col int) {
			ui.Text(c, PreviewCell(row, col)).SingleLine().FontSize(12)
		}).Grow(1)
	})
}
