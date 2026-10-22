package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
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

// mysqlObjectWorkspace lists real INFORMATION_SCHEMA objects and explicitly opens
// read-only data or structure pages on the managed session.
func (s *State) mysqlObjectWorkspace(c *ui.Context) {
	if s.mysqlSelectedDatabase == "" {
		ui.Column(c).Grow(1).Gap(5).Padding(12).Children(func() {
			if len(s.mysqlSessionDatabases) == 0 {
				ui.Text(c, "当前会话没有可见数据库").TextColor(theme.Muted)
			}
			for _, name := range s.mysqlSessionDatabases {
				db := name
				if ui.Button(c, db).Label("浏览数据库：" + db).Height(30).Clicked() {
					if s.mysqlDatabaseNodes == nil {
						s.mysqlDatabaseNodes = make(map[string]*mysqlDatabaseNode)
					}
					node := s.mysqlDatabaseNodes[db]
					if node == nil {
						node = &mysqlDatabaseNode{}
						s.mysqlDatabaseNodes[db] = node
					}
					node.open = true
					s.selectMySQLDatabase(c.Services(), db)
				}
			}
		})
		return
	}
	db := s.mysqlSelectedDatabase
	if s.mysqlObjectPending == db {
		ui.Text(c, "正在加载表和视图…").Padding(10).TextColor(theme.Muted)
		return
	}
	if err := s.mysqlObjectErrors[db]; err != "" {
		ui.Text(c, "读取对象失败："+err).Padding(10).TextColor(theme.Gold)
		return
	}
	if _, ok := s.mysqlObjectCache[db]; !ok {
		ui.Text(c, "展开数据库后按需加载元数据").Padding(10).TextColor(theme.Muted)
		return
	}
	items := s.mysqlSelectedObjects()
	if len(items) == 0 {
		ui.Text(c, "当前筛选下没有可见的表或视图").Padding(10).TextColor(theme.Muted)
		return
	}
	s.objectState.Selected = &s.objectRow
	// Double-click opens real table/view data, as observed in the original.
	// Design remains a separate explicit read-only action.
	s.objectState.CellMenu = func(m *ui.Menu, row, col int) {
		if row >= 0 && row < len(items) && m.Item("打开表（只读）").Chosen() {
			s.openMySQLData(c.Services(), db, items[row].Name)
		}
		if row >= 0 && row < len(items) && m.Item("设计表（只读）").Chosen() {
			s.openMySQLStructure(c.Services(), db, items[row].Name)
		}
	}
	if ui.Table(c, &s.objectState, mysqlObjectColumns, len(items), func(row, col int) {
		obj := items[row]
		switch col {
		case 0:
			iconName := "table"
			if obj.Kind == "视图" {
				iconName = "view"
			}
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				theme.Icon(c, iconName, 14, theme.Blue)
				ui.Text(c, obj.Name).SingleLine()
			})
		case 4:
			ui.Text(c, obj.Engine).SingleLine()
		case 6:
			ui.Text(c, obj.Comment).SingleLine()
		}
	}).Grow(1).Submitted() {
		if s.objectRow >= 0 && s.objectRow < len(items) {
			s.openMySQLData(c.Services(), db, items[s.objectRow].Name)
		}
	}
}
