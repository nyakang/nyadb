package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) workspace(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.Sidebar).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.TabHeight)).AlignItems(ui.Center).Padding(0, 3).
			Background(palette.Panel).Gap(3).Children(func() {
			ui.ScrollHorizontal(c).Grow(1).Children(func() {
				tabs := ui.TabsBase(c, &s.activeTab, len(s.tabs))
				tabs.List.Gap(0).Children(func() {
					for i, doc := range s.tabs {
						index, current := i, doc
						item := tabs.Tab(i).Height(theme.RowHeight(c, theme.TabHeight)).Padding(2, 8).Radius(0).Label(current.title)
						if i == s.activeTab {
							item.Background(palette.ToolbarSelected).BorderWidth(1, 1, 0, 1).BorderColor(palette.Border)
						}
						item.Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
								icon := "database"
								if current.kind == tabSQL {
									icon = "query"
								} else if current.kind == tabData {
									icon = "table"
								} else if current.kind == tabStructure {
									icon = "design-table"
								} else if current.kind == tabMySQLData {
									icon = "table"
								} else if current.kind == tabSettings {
									icon = "settings"
								} else if current.kind == tabProgram {
									icon = "function"
								} else if current.kind == tabConnections {
									icon = "connection"
								}
								theme.Icon(c, icon, 14, palette.Blue)
								ui.Text(c, s.workspaceTabCaption(current)).FontSize(theme.Font(c, theme.FontSize)).SingleLine().MaxWidth(220).Tooltip(current.title)
								if current.kind != tabObjects {
									if ui.ButtonBase(c).Label("关闭 "+current.title).Tooltip("关闭 "+current.title).
										Size(16, 18).Children(func() { theme.Icon(c, "close", 10, palette.Muted) }).Clicked() {
										s.closeTab(index)
									}
								}
							})
						})
						item.ContextMenu(func(m *ui.Menu) {
							if m.Item(locales.Text("关闭 ") + current.title).Disabled(index == 0).Chosen() {
								s.closeTab(index)
							}
							if m.Item(locales.Text("关闭其他页签")).Disabled(len(s.tabs) <= 1 || (len(s.tabs) == 2 && index != 0)).Chosen() {
								s.closeOtherTabs(index)
							}
						})
					}
				})
			})
			if ui.ButtonBase(c).Label(locales.Text("新建 SQL 页签")).Tooltip(locales.Text("新建 SQL 页签")).Size(24, 23).
				Children(func() { theme.Icon(c, "plus", 12, palette.Muted) }).Clicked() {
				s.openQuery()
			}
		})
		switch tab := s.currentTab(); tab.kind {
		case tabSQL:
			s.queryWorkspace(c, tab)
		case tabSettings:
			s.settingsWorkspace(c)
		case tabData:
			s.dataPreview(c)
		case tabStructure:
			s.mysqlStructureWorkspace(c, tab)
		case tabMySQLData:
			s.mysqlDataWorkspace(c, tab)
		case tabProgram:
			s.mysqlProgramWorkspace(c, tab)
		case tabConnections:
			s.connectionManagementWorkspace(c)
		default:
			s.objectsWorkspace(c)
		}
	})
}

func (s *State) objectActions(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, 31)).AlignItems(ui.Center).Gap(6).Padding(2, 6).
		Background(palette.Header).BorderWidth(0, 0, 1, 0).BorderColor(palette.Border).Children(func() {
		ui.Toolbar(c, func() {
			for _, name := range []string{"打开表", "设计表", "新建表", "删除表", "导入向导", "导出向导"} {
				action := name
				if ui.Button(c, locales.Text(action)).Padding(4, 6).Border(1, palette.Border).Radius(3).FontSize(theme.Font(c, 12)).Clicked() {
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
		}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(6).Background(palette.Header)
		if ui.Button(c, locales.Text("刷新")).Padding(3, 6).Border(1, palette.Border).Radius(3).Clicked() {
			s.status = "已刷新模拟对象列表"
		}
	})
}

func (s *State) objectsWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	if s.profileMode {
		s.mysqlDatabaseWorkspace(c)
		return
	}
	ui.Column(c).Fill().Children(func() {
		s.objectActions(c)
		items := s.visibleObjects()
		if len(items) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				theme.Icon(c, "database", 40, palette.Muted)
				ui.Text(c, locales.Text("选择左侧连接或数据库以浏览对象")).FontSize(theme.Font(c, 14)).TextColor(palette.Muted)
				ui.Text(c, locales.Text("当前为 UI 预览，数据库连接将在 M2 接入")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
			})
			return
		}
		s.objectState.Selected = &s.objectRow
		columns := append([]ui.TableColumn(nil), objectColumns...)
		for i := range columns {
			columns[i].Title = locales.Text(columns[i].Title)
		}
		if theme.Table(c, &s.objectState, columns, len(items), func(row, col int) {
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
					theme.Icon(c, symbol, 14, palette.Blue)
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
	if s.profileMode {
		s.mysqlQueryWorkspace(c,tab)
		return
	}
	palette := theme.Colors(c)

	ui.SplitVerticalPercent(c, &s.editorPercent,
		func() {
			ui.Column(c).Fill().Children(func() {
				ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).
					Background(palette.Header).Children(func() {
					ui.Toolbar(c, func() {
						if ui.Button(c, locales.Text("运行模拟查询")).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
							s.demoRuns++
							s.status = fmt.Sprintf("模拟查询已运行 %d 次（没有访问数据库）", s.demoRuns)
						}
						ui.Button(c, locales.Text("取消")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(true)
					}).Label(locales.Text("查询操作")).Grow(1).MinWidth(0).Padding(0).Gap(4)
					ui.Text(c, "query.sql").TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
				})
				tab.editor.Theme = theme.SQLThemeFor(c)
				editor.View(c, tab.editor).Grow(1).FillWidth()
			})
		},
		func() { s.dataPreview(c) },
	).Grow(1)
}

func (s *State) dataPreview(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).AlignItems(ui.Center).Padding(2, 6).Gap(8).
			Background(palette.Header).Children(func() {
			ui.Text(c, locales.Text("数据预览")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, locales.Text("模拟数据 · 100,000 行 / 50 列")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Grow(1).MinWidth(0)
		})
		theme.Table(c, &s.dataState, s.dataColumns, PreviewRows, func(row, col int) {
			ui.Text(c, PreviewCell(row, col)).SingleLine().FontSize(theme.Font(c, 12))
		}).Grow(1)
	})
}
