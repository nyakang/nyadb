package objectworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var mysqlObjectColumns = []ui.TableColumn{
	{Title: "名称", Width: 265},
	{Title: "自动递增值", Width: 100},
	{Title: "修改日期", Width: 130},
	{Title: "数据长度", Width: 110},
	{Title: "引擎", Width: 95},
	{Title: "行", Width: 70},
	{Title: "注释", Width: 180},
}

// MySQL's information_schema.TABLE_ROWS is not a reliable exact count for
// InnoDB. The visible approximation mark prevents confusing it with COUNT(*).
// NULL from the server remains empty, but a measured zero remains "0".
func mysqlObjectCellText(obj contract.DBObject, col int) string {
	number := func(n *int64) string {
		if n == nil {
			return ""
		}
		return strconv.FormatInt(*n, 10)
	}
	switch col {
	case 0:
		return obj.Name
	case 1:
		return number(obj.AutoIncrement)
	case 2:
		return obj.ModifiedAt
	case 3:
		return number(obj.DataLength)
	case 4:
		return obj.Engine
	case 5:
		if obj.RowsEstimate != nil {
			return "≈" + number(obj.RowsEstimate)
		}
	case 6:
		return obj.Comment
	}
	return ""
}

func mysqlObjectActionLabels(kind string) (open, design string) {
	if Supports(kind) {
		return "查看定义（只读）", "查看结构（不适用）"
	}
	if kind == "Core" {
		return "打开文档", "查看 Schema（只读）"
	}
	if kind == "索引" || kind == "别名" || kind == "数据流" {
		return "打开文档", "查看映射（只读）"
	}
	if kind == "键" {
		return "打开键", "查看键结构（只读）"
	}
	if kind == "集合" {
		return "打开集合", "查看集合结构（只读）"
	}
	if kind == "视图" {
		return "打开视图（只读）", "查看视图结构（只读）"
	}
	return "打开表", "设计表（只读）"
}

// mysqlObjectWorkspace lists real INFORMATION_SCHEMA objects and opens table
// data with editing support; view data and structure pages remain read-only.
func (s *State) mysqlObjectWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	b := s.currentBrowser()
	db := b.Database
	if !b.Connected || db == "" {
		ui.Box(c).Grow(1)
		return
	}
	if !b.Opened {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, locales.Text("双击打开数据库")).Padding(8).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted)
		})
		return
	}
	items := s.SelectedObjects()
	message := ""
	if len(items) == 0 && b.Pending != db && b.Errors[db] == "" {
		message = "当前筛选下没有可见的数据库对象"
	}
	s.currentBrowser().List.Selected = &s.currentBrowser().Row
	s.currentBrowser().List.Selection = &s.currentBrowser().selection
	s.currentBrowser().List.Key = func(row int) any {
		if row < 0 || row >= len(items) {
			return nil
		}
		obj := items[row]
		return objectKey{s.currentBrowser().ProfileID, db, obj.Kind, obj.Name}
	}
	// Double-click opens real table/view data, as observed in the original.
	// Design remains a separate explicit read-only action.
	s.currentBrowser().List.CellMenu = func(m *ui.Menu, row, col int) {
		if row < 0 || row >= len(items) {
			return
		}
		s.currentBrowser().Row = row
		s.selected(db, items[row].Name, items[row].Kind)
		s.ContextMenu(c.Services(), m, s.currentBrowser().ProfileID, db, items[row].Name, items[row].Kind)
	}
	if b.Pending == db || b.Errors[db] != "" {
		ui.Row(c).Padding(3, 8).Gap(6).AlignItems(ui.Center).Children(func() {
			if b.Pending == db {
				ui.Text(c, locales.Text("正在读取数据库对象…")).TextColor(palette.Muted)
				if ui.Button(c, locales.Text("取消加载")).Clicked() {
					s.cancelObjects()
				}
			} else {
				ui.Text(c, locales.Text("读取对象失败：")+b.Errors[db]).TextColor(palette.Error)
				if ui.Button(c, locales.Text("重试")).Clicked() {
					s.loadObjects(c.Services(), db, true)
				}
			}
		})
	}
	columns := s.objectColumns(b.ProfileID)
	if len(items) == 0 {
		theme.EmptyTable(c, &s.currentBrowser().List, columns, message)
		return
	}
	previousRow := s.currentBrowser().Row
	table := theme.Table(c, &s.currentBrowser().List, columns, len(items), func(row, col int) {
		obj := items[row]
		switch col {
		case 0:
			iconName := "table"
			if obj.Kind == "视图" {
				iconName = "view"
			} else if obj.Kind == "函数" || obj.Kind == "存储过程" {
				iconName = "function"
			} else if obj.Kind == "事件" || obj.Kind == "序列" {
				iconName = "refresh"
			}
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				theme.Icon(c, iconName, 14, palette.Blue)
				s.objectNameCell(c, obj)
			})
		default:
			ui.Text(c, s.objectCell(b.ProfileID, obj, col)).SingleLine()
		}
	}).Grow(1)
	if table.Submitted() {
		if s.currentBrowser().Row >= 0 && s.currentBrowser().Row < len(items) {
			s.openObject(c.Services(), db, items[s.currentBrowser().Row].Name, items[s.currentBrowser().Row].Kind)
		}
	}
	if s.currentBrowser().Row >= 0 && s.currentBrowser().Row < len(items) {
		if previousRow != s.currentBrowser().Row || table.Changed() || table.Focused() {
			s.selected(db, items[s.currentBrowser().Row].Name, items[s.currentBrowser().Row].Kind)
		}
		if table.Shortcut(ui.Ctrl, ui.KeyD) && !Supports(items[s.currentBrowser().Row].Kind) {
			s.openStructure(c.Services(), db, items[s.currentBrowser().Row].Name)
		}
		if items[s.currentBrowser().Row].Kind == "表" {
			if table.Shortcut(0, ui.KeyF2) {
				s.BeginInlineRename(items[s.currentBrowser().Row].Name, items[s.currentBrowser().Row].Kind)
			}
			if table.Shortcut(0, ui.KeyDelete) {
				s.BeginTableChange(s.currentBrowser().ProfileID, db, items[s.currentBrowser().Row].Name, mysqlsvc.MySQLTableDrop)
			}
		}
	}
	table.TrackScroll(&b.Scroll)
	if table.Shortcut(ui.Ctrl, ui.KeyN) && (s.currentBrowser().Kind == "全部" || s.currentBrowser().Kind == "表") {
		s.BeginTableChange(s.currentBrowser().ProfileID, db, "", mysqlsvc.MySQLTableCreate)
	}
	if table.Shortcut(0, ui.KeyF5) && b.Pending == "" && !b.Refreshing {
		s.loadObjects(c.Services(), db, true)
	}

}

func (s *State) BrowserView(c *ui.Context) {
	palette := theme.Colors(c)

	s.validateGroupDialog()
	a := s.browserActions()
	database := a.scope.database
	items := a.items
	redisEngine := s.deps.Engine != nil && s.deps.Engine(a.scope.profile) == "redis"
	searchEngine := s.deps.Engine != nil && (s.deps.Engine(a.scope.profile) == "elasticsearch" || s.deps.Engine(a.scope.profile) == "easysearch" || s.deps.Engine(a.scope.profile) == "solr")
	documentEngine := s.deps.Engine != nil && s.deps.Engine(a.scope.profile) == "mongodb"
	canOpen, canProgram := a.canOpen, a.canProgram
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 30)).Gap(3).Padding(1, 4).AlignItems(ui.Center).Background(palette.Header).Children(func() {
			ui.Toolbar(c, func() {
				// Keep available object actions visible on narrow pages.
				for _, action := range []struct{ label, icon, accessible string }{
					{"打开表", "open-table", "打开选中表数据"}, {"设计表", "design-table", "设计选中表"}, {"查看结构", "design-table", "打开选中表结构"},
				} {
					item := action
					if searchEngine {
						if item.label == "设计表" {
							continue
						}
						if item.label == "打开表" {
							item.label = "打开文档"
							item.accessible = "打开选中对象文档"
						}
						if item.label == "查看结构" {
							item.accessible = "查看映射（只读）"
							if s.deps.Engine(a.scope.profile) == "solr" {
								item.accessible = "查看 Schema（只读）"
							}
						}
					}
					if redisEngine {
						if item.label == "设计表" {
							continue
						}
						if item.label == "打开表" {
							item.label = "打开键"
							item.accessible = "打开选中键数据"
						}
						if item.label == "查看结构" {
							item.accessible = "查看键结构（只读）"
						}
					}
					if documentEngine {
						if item.label == "设计表" {
							continue
						}
						if item.label == "打开表" {
							item.label = "打开集合"
							item.accessible = "打开选中集合文档"
						}
						if item.label == "查看结构" {
							item.accessible = "查看集合结构（只读）"
						}
					}
					if ui.Button(c, "").Label(locales.Text(item.accessible)).Tooltip(locales.Text(item.label)).Disabled(!canOpen).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							theme.Icon(c, item.icon, 14, palette.Muted)
							ui.Text(c, locales.Text(item.label)).FontSize(theme.Font(c, 12)).SingleLine()
						})
					}).Clicked() {
						obj := items[a.frame.Row]
						if (item.label == "打开表" || item.label == "打开集合" || item.label == "打开键" || item.label == "打开文档") && s.deps.OpenData != nil {
							s.deps.OpenData(c.Services(), a.scope.profile, database, obj.Name)
						} else if item.label == "查看结构" {
							s.openStructure(c.Services(), database, obj.Name)
						} else if item.label == "设计表" {
							s.openDesign(c.Services(), a.scope.profile, database, obj.Name)
						}
					}
				}
				canCreate := a.canCreate
				if !documentEngine && !redisEngine && !searchEngine {
					if ui.Button(c, "").Label(locales.Text("新建表")).Tooltip(locales.Text("新建表")).
						Disabled(!canCreate).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							theme.Icon(c, "table", 14, palette.Muted)
							ui.Text(c, locales.Text("新建表")).FontSize(theme.Font(c, 12)).SingleLine()
						})
					}).Clicked() {
						s.BeginTableChange(a.scope.profile, database, "", mysqlsvc.MySQLTableCreate)
					}
					if ui.Button(c, "").Label(locales.Text("查看程序定义")).Tooltip(locales.Text("查看函数、存储过程、事件或序列定义")).
						Disabled(!canProgram).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
						theme.Icon(c, "function", 14, palette.Muted)
					}).Clicked() {
						obj := items[a.frame.Row]
						s.openObjectFor(c.Services(), a.scope.profile, database, obj.Name, obj.Kind)
					}
				}
				kinds := []string{"全部", "表", "视图"}
				if searchEngine {
					kinds = []string{"全部", "索引", "别名", "数据流"}
					if s.deps.Engine(a.scope.profile) == "solr" {
						kinds = []string{"全部", "Core", "集合", "别名"}
					}
				}
				if redisEngine {
					kinds = []string{"全部", "键"}
				}
				if documentEngine {
					kinds = []string{"全部", "集合", "视图"}
				}
				chosen := 0
				for i, kind := range kinds {
					if a.frame.Kind == kind {
						chosen = i
					}
				}
				parts := ui.SegmentedBase(c, &chosen, len(kinds))
				parts.Track.Label(locales.Text("筛选对象")).Disabled(!a.ready).Gap(2).Children(func() {
					for i, kind := range kinds {
						parts.Segment(i).Label(locales.Text("筛选对象：")+locales.Text(kind)).MinHeight(theme.RowHeight(c, 25)).Padding(2, 5).Children(func() { ui.Text(c, locales.Text(kind)).SingleLine() })
					}
				})
				if parts.Track.Changed() {
					s.Filter(kinds[chosen])
				}

			}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(2).Background(palette.Header)
			ui.Button(c, "").Label(locales.Text("更多对象操作")).Tooltip(locales.Text("更多对象操作")).
				Disabled(!a.ready && !a.canAccounts && !a.canResume).Padding(2, 5).Height(theme.RowHeight(c, 25)).
				Children(func() { ui.Text(c, locales.Text("更多")).FontSize(theme.Font(c, 12)).SingleLine() }).
				Menu(func(m *ui.Menu) { s.browserMoreMenu(c.Services(), m) })
			// One refresh affordance follows the current scope.
			label := locales.Text("刷新数据库列表")
			if database != "" {
				label = locales.Text("刷新元数据：") + database
			}
			if ui.Button(c, "").Label(label).Tooltip(locales.Text("刷新")).Disabled(!a.canRefresh).Size(25, 25).Children(func() {
				color := palette.Muted
				if a.canRefresh {
					color = palette.Blue
				}
				theme.Icon(c, "refresh", 14, color)
			}).Clicked() {
				if database == "" {
					s.refreshDatabases(c.Services())
				} else {
					s.loadObjects(c.Services(), database, true)
				}
			}
		})
		if a.frame.Connected && a.frame.Error != "" {
			ui.Text(c, a.frame.Error).Padding(5, 8).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
		}
		s.mysqlObjectWorkspace(c)
	})
}

// ObjectProperties is the information panel for cached object metadata.
func ObjectProperties(c *ui.Context, obj contract.DBObject) {
	theme.Property(c, "引擎", obj.Engine)
	theme.Property(c, "注释", obj.Comment)
	if obj.RootPage != nil {
		theme.Property(c, "根页面", fmt.Sprint(*obj.RootPage))
	}
	if Supports(obj.Kind) {
		for _, field := range programProperties(obj) {
			if field.label == "类型" || field.label == "名称" || field.label == "注释" {
				continue
			}
			if field.value != "" {
				theme.Property(c, field.label, field.value)
			}
		}
	}
}
