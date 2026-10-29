package shell

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
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
	if isMySQLProgram(kind) {
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

	db := s.sessions.Current().Database
	items := s.sessions.SelectedObjects()
	connected := s.sessions.StatusFor(s.selectedProfileID) == "已连接"
	if !connected || db == "" {
		items = nil
	}
	message := ""
	if connected && db != "" {
		if !s.sessions.DatabaseOpened(s.sessions.Current().ProfileID, db) {
			items = nil
			message = locales.Text("双击打开数据库")
		} else if s.sessions.Current().Pending == db {
			message = "正在读取数据库对象…"
		} else if err := s.sessions.Current().Errors[db]; err != "" {
			message = "读取对象失败：" + err
		} else if len(items) == 0 {
			message = "当前筛选下没有可见的数据库对象"
		}
	}
	s.sessions.Current().List.Selected = &s.sessions.Current().Row
	s.sessions.Current().List.Key = func(row int) any {
		if row < 0 || row >= len(items) {
			return nil
		}
		obj := items[row]
		return (navigation.Node{Kind: "object", ProfileID: s.sessions.Current().ProfileID, Database: db, Category: obj.Kind, Object: obj.Name}).Key()
	}
	// Double-click opens real table/view data, as observed in the original.
	// Design remains a separate explicit read-only action.
	s.sessions.Current().List.CellMenu = func(m *ui.Menu, row, col int) {
		if row < 0 || row >= len(items) {
			return
		}
		s.sessions.Current().Row = row
		s.selectObjectInNavigator(db, items[row].Name, items[row].Kind)
		s.mysqlObjectContextMenu(c.Services(), m, s.sessions.Current().ProfileID, db, items[row].Name, items[row].Kind)
	}
	columns := append([]ui.TableColumn(nil), mysqlObjectColumns...)
	for i := range columns {
		columns[i].ID = mysqlObjectColumns[i].Title
		columns[i].Title = locales.Text(columns[i].Title)
	}
	if len(items) == 0 {
		emptyTableView(c, &s.sessions.Current().List, columns, message)
		if connected && db != "" && s.sessions.Current().Errors[db] != "" && ui.Button(c, locales.Text("重试")).Clicked() {
			s.loadMySQLObjects(c.Services(), db, true)
		}
		if connected && db != "" && s.sessions.Current().Pending == db && ui.Button(c, locales.Text("取消加载")).Clicked() {
			s.sessions.CancelObjects()
			s.sessions.Current().Errors[db] = "加载已取消"
		}
		return
	}
	previousRow := s.sessions.Current().Row
	table := theme.Table(c, &s.sessions.Current().List, columns, len(items), func(row, col int) {
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
				ui.Text(c, obj.Name).SingleLine()
			})
		default:
			ui.Text(c, mysqlObjectCellText(obj, col)).SingleLine()
		}
	}).Grow(1)
	if table.Submitted() {
		if s.sessions.Current().Row >= 0 && s.sessions.Current().Row < len(items) {
			s.openMySQLObject(c.Services(), db, items[s.sessions.Current().Row].Name, items[s.sessions.Current().Row].Kind)
		}
	}
	if s.sessions.Current().Row >= 0 && s.sessions.Current().Row < len(items) {
		if previousRow != s.sessions.Current().Row || table.Changed() || (table.Focused() && navigation.Parse(s.navigation.Key).Kind != "object") {
			s.selectObjectInNavigator(db, items[s.sessions.Current().Row].Name, items[s.sessions.Current().Row].Kind)
		}
		if table.Shortcut(ui.Ctrl, ui.KeyD) && !isMySQLProgram(items[s.sessions.Current().Row].Kind) {
			s.openMySQLStructure(c.Services(), db, items[s.sessions.Current().Row].Name)
		}
		if items[s.sessions.Current().Row].Kind == "表" {
			if table.Shortcut(0, ui.KeyF2) {
				s.beginMySQLTableChange(s.sessions.Current().ProfileID, db, items[s.sessions.Current().Row].Name, mysqlsvc.MySQLTableRename)
			}
			if table.Shortcut(0, ui.KeyDelete) {
				s.beginMySQLTableChange(s.sessions.Current().ProfileID, db, items[s.sessions.Current().Row].Name, mysqlsvc.MySQLTableDrop)
			}
		}
	}
	if table.Shortcut(ui.Ctrl, ui.KeyN) && (s.sessions.Current().Kind == "全部" || s.sessions.Current().Kind == "表") {
		s.beginMySQLTableChange(s.sessions.Current().ProfileID, db, "", mysqlsvc.MySQLTableCreate)
	}
	if table.Shortcut(0, ui.KeyF5) && s.sessions.Current().Pending == "" && connected {
		s.loadMySQLObjects(c.Services(), db, true)
	}

}

func (s *State) mysqlDatabaseWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	p, ok := s.currentProfile()
	connected := ok && s.sessions.StatusFor(p.ID) == "已连接"
	database := s.sessions.Current().Database
	items := s.sessions.SelectedObjects()
	canOpen := connected && database != "" && s.sessions.Current().Row >= 0 && s.sessions.Current().Row < len(items)
	canProgram := canOpen && isMySQLProgram(items[s.sessions.Current().Row].Kind)
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
						obj := items[s.sessions.Current().Row]
						if item.label == "打开表" {
							s.openMySQLData(c.Services(), database, obj.Name)
						} else {
							s.openMySQLStructure(c.Services(), database, obj.Name)
						}
					}
				}
				canCreate := database != "" && s.canChangeMySQLTable(s.sessions.Current().ProfileID) &&
					(s.sessions.Current().Kind == "全部" || s.sessions.Current().Kind == "表")
				if ui.ButtonBase(c).Label(locales.Text("新建表")).Tooltip(locales.Text("新建表")).
					Disabled(!canCreate).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						theme.Icon(c, "table", 14, palette.Muted)
						ui.Text(c, locales.Text("新建表")).FontSize(theme.Font(c, 12)).SingleLine()
					})
				}).Clicked() {
					s.beginMySQLTableChange(s.sessions.Current().ProfileID, database, "", mysqlsvc.MySQLTableCreate)
				}
				if ui.ButtonBase(c).Label("查看程序定义").Tooltip("查看函数、存储过程或事件定义").
					Disabled(!canProgram).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
					theme.Icon(c, "function", 14, palette.Muted)
				}).Clicked() {
					obj := items[s.sessions.Current().Row]
					s.openMySQLProgram(database, obj.Name, obj.Kind)
				}
				for _, kind := range []string{"全部", "表", "视图"} {
					filter := kind
					button := ui.ButtonBase(c).Label("筛选对象："+filter).Disabled(!connected || !s.sessions.DatabaseOpened(s.sessions.Current().ProfileID, database)).Height(theme.RowHeight(c, 25)).Padding(2, 5).Children(func() { ui.Text(c, locales.Text(filter)).FontSize(theme.Font(c, 11)) })
					if connected && s.sessions.Current().Kind == filter {
						button.Background(palette.Border.Alpha(.35))
					}
					if button.Clicked() {
						s.filterMySQLObjects(filter)
					}
				}
			}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(2).Background(palette.Header)
			// One refresh affordance follows the current scope.
			label := "刷新数据库列表"
			if database != "" {
				label = "刷新元数据：" + database
			}
			if ui.ButtonBase(c).Label(label).Tooltip(locales.Text("刷新")).Disabled(!connected || s.sessions.Current().Refreshing() || s.sessions.Current().Pending != "").Size(25, 25).Children(func() { theme.Icon(c, "refresh", 14, palette.Blue) }).Clicked() {
				if database == "" {
					s.sessions.RefreshDatabases(c.Services().Invalidate)
				} else {
					s.openMySQLDatabase(c.Services(), database, true)
				}
			}
		})
		if connected && s.sessions.Current().Error != "" {
			ui.Text(c, s.sessions.Current().Error).Padding(5, 8).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
		}
		s.mysqlObjectWorkspace(c)
	})
}

func (s *State) selectMySQLDatabase(services ui.Services, database string) {
	s.openMySQLDatabase(services, database, false)
}

func (s *State) openMySQLDatabase(services ui.Services, database string, force bool) {
	s.sessions.SelectDatabase(database)
	if database == "" || s.sessions.Current().Status != "已连接" {
		return
	}
	s.sessions.MarkOpened(s.sessions.Current().ProfileID, database)
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.sessions.Current().ProfileID))
	s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key())
	s.loadMySQLObjects(services, database, force)
}

// Keep the selected object's qualified identity when it survives a filter.
// If it does not, select the visible category/database instead of leaving an
// inspector on an object that the central list no longer contains.
func (s *State) filterMySQLObjects(kind string) {
	if kind == s.sessions.Current().Kind {
		return
	}
	selected := navigation.Parse(s.navigation.Key)
	s.sessions.Current().Kind = kind
	s.sessions.Current().List = ui.ListState{Columns: s.sessions.Current().List.Columns}
	s.sessions.Current().Row = -1
	if selected.Kind == "object" && selected.Database == s.sessions.Current().Database &&
		(kind == "全部" || kind == selected.Category) {
		for i, obj := range s.sessions.SelectedObjects() {
			if obj.Name == selected.Object && obj.Kind == selected.Category {
				s.sessions.Current().Row = i
				return
			}
		}
	}
	if s.sessions.Current().Database != "" {
		node := navigation.DatabaseNode(s.sessions.Current().ProfileID, s.sessions.Current().Database)
		if kind != "全部" {
			node.Kind, node.Category = "category", kind
		}
		s.revealNavigationNode(node)
	}
}

func (s *State) mysqlObjectsChanged(id int64, database string) {
	if id != s.sessions.Current().ProfileID {
		return
	}
	if database == s.sessions.Current().Database {
		selected := navigation.Parse(s.navigation.Key)
		items := s.sessions.SelectedObjects()
		if selected.Kind == "object" && selected.Database == database {
			s.sessions.Current().Row = -1
			for i, obj := range items {
				if obj.Name == selected.Object && obj.Kind == selected.Category {
					s.sessions.Current().Row = i
					break
				}
			}
			if s.sessions.Current().Row < 0 {
				s.navigation.Key = navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key()
				s.navigation.Row = -1
			}
		} else if s.sessions.Current().Row < 0 && len(items) > 0 {
			s.sessions.Current().Row = 0
		}
	}
	s.resolveNavigatorObject()
}

func (s *State) loadMySQLObjects(services ui.Services, database string, force bool) {
	if database != "" && s.sessions.Current().Status == "已连接" && s.sessions.Current().ProfileID > 0 && !s.sessions.DatabaseOpened(s.sessions.Current().ProfileID, database) {
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.sessions.Current().ProfileID))
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key())
	}
	s.sessions.LoadObjects(database, force, services.Invalidate)
}
