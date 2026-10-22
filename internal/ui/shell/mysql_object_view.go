package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

var mysqlObjectColumns = []ui.TableColumn{
	{Title: "名称", Width: 250},
	{Title: "类型", Width: 85},
	{Title: "引擎", Width: 110},
	{Title: "注释", Width: 290},
}

// mysqlObjectWorkspace only renders real INFORMATION_SCHEMA results. No
// query execution or table-data preview can be triggered by these rows.
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
	ui.Row(c).Padding(5, 10).Gap(5).AlignItems(ui.Center).Children(func() {
		for i, name := range s.mysqlSessionDatabases {
			if i >= 12 {
				break
			} // The complete list remains available in the native sidebar tree.
			selected := name
			if ui.Button(c, selected).Label("切换数据库：" + selected).Clicked() {
				s.selectMySQLDatabase(c.Services(), selected)
			}
		}
	})
	ui.Row(c).Padding(6, 10).Gap(10).AlignItems(ui.Center).Children(func() {
		theme.Icon(c, "database", 16, theme.Blue)
		ui.Text(c, db).Bold().FontSize(13)
		for _, kind := range []string{"全部", "表", "视图"} {
			k := kind
			if ui.Button(c, k).Label("筛选对象：" + k).Clicked() {
				s.mysqlObjectKind = k
			}
		}
		ui.Box(c).Grow(1)
		items := s.mysqlSelectedObjects()
		canOpen := s.objectRow >= 0 && s.objectRow < len(items)
		if ui.Button(c, "设计表（只读）").Label("打开选中表结构").Disabled(!canOpen).Clicked() {
			s.openMySQLStructure(c.Services(), db, items[s.objectRow].Name)
		}
		if ui.Button(c, "刷新当前数据库").Label("刷新元数据：" + db).
			Disabled(s.mysqlObjectPending != "").Clicked() {
			s.loadMySQLObjects(c.Services(), db, true)
		}
	})
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
	// The original table list exposes "设计表" on right click. Until the real
	// data viewer exists, keep double-click's structure fallback explicit in
	// the differential report; do not invent a fake table-data page.
	s.objectState.CellMenu = func(m *ui.Menu, row, col int) {
		if row >= 0 && row < len(items) && m.Item("设计表（只读）").Chosen() {
			s.openMySQLStructure(c.Services(), db, items[row].Name)
		}
	}
	if ui.Table(c, &s.objectState, mysqlObjectColumns, len(items), func(row, col int) {
		obj := items[row]
		switch col {
		case 0:
			ui.Text(c, obj.Name).SingleLine()
		case 1:
			ui.Text(c, obj.Kind).SingleLine()
		case 2:
			ui.Text(c, obj.Engine).SingleLine()
		case 3:
			ui.Text(c, obj.Comment).SingleLine()
		}
	}).Grow(1).Submitted() {
		if s.objectRow >= 0 && s.objectRow < len(items) {
			s.openMySQLStructure(c.Services(), db, items[s.objectRow].Name)
		}
	}
}
