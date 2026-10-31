package objectworkspace

import (
	"strconv"

	"github.com/egoist/mygo/ui"

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
func mysqlObjectCellText(obj mysqlsvc.MySQLDBObject, col int) string {
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
	if kind == "视图" {
		return "打开视图（只读）", "查看视图结构（只读）"
	}
	return "打开表", "设计表（只读）"
}

// mysqlObjectWorkspace lists real INFORMATION_SCHEMA objects and opens table
// data with editing support; view data and structure pages remain read-only.
func (s *State) mysqlObjectWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	db := s.currentBrowser().Database
	items := s.SelectedObjects()
	connected := s.currentBrowser().Connected
	if !connected || db == "" {
		items = nil
	}
	message := ""
	if connected && db != "" {
		if !s.currentBrowser().Opened {
			items = nil
			message = locales.Text("双击打开数据库")
		} else if s.currentBrowser().Pending == db {
			message = "正在读取数据库对象…"
		} else if err := s.currentBrowser().Errors[db]; err != "" {
			message = "读取对象失败：" + err
		} else if len(items) == 0 {
			message = "当前筛选下没有可见的数据库对象"
		}
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
	columns := append([]ui.TableColumn(nil), mysqlObjectColumns...)
	for i := range columns {
		columns[i].ID = mysqlObjectColumns[i].Title
		columns[i].Title = locales.Text(columns[i].Title)
	}
	if len(items) == 0 {
		theme.EmptyTable(c, &s.currentBrowser().List, columns, message)
		if connected && db != "" && s.currentBrowser().Errors[db] != "" && ui.Button(c, locales.Text("重试")).Clicked() {
			s.loadObjects(c.Services(), db, true)
		}
		if connected && db != "" && s.currentBrowser().Pending == db && ui.Button(c, locales.Text("取消加载")).Clicked() {
			s.cancelObjects()

		}
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
			} else if obj.Kind == "事件" {
				iconName = "refresh"
			}
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				theme.Icon(c, iconName, 14, palette.Blue)
				s.objectNameCell(c, obj)
			})
		default:
			ui.Text(c, mysqlObjectCellText(obj, col)).SingleLine()
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
	if table.Shortcut(ui.Ctrl, ui.KeyN) && (s.currentBrowser().Kind == "全部" || s.currentBrowser().Kind == "表") {
		s.BeginTableChange(s.currentBrowser().ProfileID, db, "", mysqlsvc.MySQLTableCreate)
	}
	if table.Shortcut(0, ui.KeyF5) && s.currentBrowser().Pending == "" && connected {
		s.loadObjects(c.Services(), db, true)
	}

}

func (s *State) BrowserView(c *ui.Context) {
	palette := theme.Colors(c)

	connected := s.currentBrowser().Connected
	database := s.currentBrowser().Database
	items := s.SelectedObjects()
	canOpen := connected && database != "" && s.currentBrowser().Row >= 0 && s.currentBrowser().Row < len(items)
	canProgram := canOpen && Supports(items[s.currentBrowser().Row].Kind)
	canOpen = canOpen && !canProgram
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 30)).Gap(3).Padding(1, 4).AlignItems(ui.Center).Background(palette.Header).Children(func() {
			ui.Toolbar(c, func() {
				// Keep available object actions visible on narrow pages.
				for _, action := range []struct{ label, icon, accessible string }{
					{"打开表", "open-table", "打开选中表数据"}, {"设计表", "design-table", "打开选中表结构"},
				} {
					item := action
					if ui.ButtonBase(c).Label(locales.Text(item.accessible)).Tooltip(locales.Text(item.label)).Disabled(!canOpen).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							theme.Icon(c, item.icon, 14, palette.Muted)
							ui.Text(c, locales.Text(item.label)).FontSize(theme.Font(c, 12)).SingleLine()
						})
					}).Clicked() {
						obj := items[s.currentBrowser().Row]
						if item.label == "打开表" {
							s.openData(c.Services(), database, obj.Name)
						} else {
							s.openStructure(c.Services(), database, obj.Name)
						}
					}
				}
				canCreate := database != "" && s.CanChangeTable(s.currentBrowser().ProfileID) &&
					(s.currentBrowser().Kind == "全部" || s.currentBrowser().Kind == "表")
				if ui.ButtonBase(c).Label(locales.Text("新建表")).Tooltip(locales.Text("新建表")).
					Disabled(!canCreate).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						theme.Icon(c, "table", 14, palette.Muted)
						ui.Text(c, locales.Text("新建表")).FontSize(theme.Font(c, 12)).SingleLine()
					})
				}).Clicked() {
					s.BeginTableChange(s.currentBrowser().ProfileID, database, "", mysqlsvc.MySQLTableCreate)
				}
				if ui.ButtonBase(c).Label("查看程序定义").Tooltip("查看函数、存储过程或事件定义").
					Disabled(!canProgram).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
					theme.Icon(c, "function", 14, palette.Muted)
				}).Clicked() {
					obj := items[s.currentBrowser().Row]
					s.openProgram(database, obj.Name, obj.Kind)
				}
				for _, kind := range []string{"全部", "表", "视图"} {
					filter := kind
					button := ui.ButtonBase(c).Label("筛选对象："+filter).Disabled(!connected || !s.currentBrowser().Opened).Height(theme.RowHeight(c, 25)).Padding(2, 5).Children(func() { ui.Text(c, locales.Text(filter)).FontSize(theme.Font(c, 11)) })
					if connected && s.currentBrowser().Kind == filter {
						button.Background(palette.Border.Alpha(.35))
					}
					if button.Clicked() {
						s.Filter(filter)
					}
				}
			}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(2).Background(palette.Header)
			// One refresh affordance follows the current scope.
			label := "刷新数据库列表"
			if database != "" {
				label = "刷新元数据：" + database
			}
			if ui.ButtonBase(c).Label(label).Tooltip(locales.Text("刷新")).Disabled(!connected || s.currentBrowser().Refreshing || s.currentBrowser().Pending != "").Size(25, 25).Children(func() { theme.Icon(c, "refresh", 14, palette.Blue) }).Clicked() {
				if database == "" {
					s.refreshDatabases(c.Services())
				} else {
					s.loadObjects(c.Services(), database, true)
				}
			}
		})
		if connected && s.currentBrowser().Error != "" {
			ui.Text(c, s.currentBrowser().Error).Padding(5, 8).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
		}
		ui.Row(c).Gap(6).Padding(3, 6).Children(func() {
			if ui.Button(c, "删除所选对象…").Disabled(!connected || s.deps.BatchExecutor == nil || !s.capabilities(s.currentBrowser().ProfileID).DesignTables || len(s.SelectedObjectIdentities()) == 0).Clicked() {
				s.BeginObjectBatch(s.currentBrowser().ProfileID, s.SelectedObjectIdentities(), "drop")
			}
			if ui.Button(c, "粘贴对象…").Disabled(!connected || s.deps.BatchExecutor == nil || !s.capabilities(s.currentBrowser().ProfileID).DesignTables).Clicked() {
				clip, e := DecodeObjectClipboard(c.Services().ReadClipboard())
				if e != nil {
					s.report(e.Error())
				} else if clip.ProfileID != s.currentBrowser().ProfileID {
					s.report("对象剪贴板属于另一连接，请显式切换到原连接")
				} else {
					s.BeginObjectBatch(s.currentBrowser().ProfileID, clip.Objects, "copy")
				}
			}
			if ui.Button(c, "复制所选对象").Disabled(len(s.SelectedObjectIdentities()) == 0).Clicked() {
				raw, e := EncodeObjectClipboardFor(s.currentBrowser().ProfileID, s.SelectedObjectIdentities())
				if e == nil {
					c.WriteClipboard(raw)
				}
			}

			if ui.Button(c, "用户与权限").Disabled(!connected || s.deps.AccountExecutor == nil || !s.capabilities(s.currentBrowser().ProfileID).DesignTables).Clicked() {
				s.OpenAccounts(c.Services(), s.currentBrowser().ProfileID, database, "")
			}
			if ui.Button(c, "继续对象草稿").Disabled(s.parkedObjectOp == nil).Clicked() {
				s.ResumeObjectDraft()
			}

		})
		ui.Row(c).Gap(6).Padding(3, 6).Children(func() {
			ui.TextInput(c, &s.currentBrowser().groupDraft).Label("对象分组名称").Width(150)
			if ui.Button(c, "分组所选对象").Disabled(len(s.SelectedObjectIdentities()) == 0).Clicked() {
				s.AssignObjectGroup(s.currentBrowser().groupDraft)
			}
			s.objectGroupsView(c)
		})
		ui.Row(c).Gap(6).Padding(3, 6).Children(func() {
			if s.deps.ObjectExecutor != nil && s.capabilities(s.currentBrowser().ProfileID).DesignTables {
				for _, kind := range []string{"视图", "函数", "存储过程", "事件"} {
					k := kind
					if ui.Button(c, "新建"+k).Disabled(!connected || database == "").Clicked() {
						name := s.NextObjectName(s.currentBrowser().ProfileID, "new_object")
						s.BeginObject(c.Services(), s.currentBrowser().ProfileID, mysqlsvc.ObjectIdentity{Database: database, Name: name, Kind: k}, "create")
					}
				}
			}
		})
		s.mysqlObjectWorkspace(c)
	})
}

// ObjectProperties is the information panel for cached object metadata.
func ObjectProperties(c *ui.Context, obj mysqlsvc.MySQLDBObject) {
	theme.Property(c, "引擎", obj.Engine)
	theme.Property(c, "注释", obj.Comment)
	if Supports(obj.Kind) {
		for _, field := range []struct{ label, value string }{
			{"定义者", obj.Definer}, {"参数", obj.Parameters},
			{"返回类型", obj.ReturnType}, {"SQL SECURITY", obj.SQLSecurity},
			{"状态", obj.Status}, {"调度", obj.Schedule},
			{"开始时间", obj.Starts}, {"结束时间", obj.Ends},
			{"ON COMPLETION", obj.OnCompletion}, {"创建时间", obj.CreatedAt},
			{"修改时间", obj.ModifiedAt},
		} {
			if field.value != "" {
				theme.Property(c, field.label, field.value)
			}
		}
	}
}
