package shell

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
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
func mysqlObjectCellText(obj connections.MySQLDBObject, col int) string {
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
	if kind == "视图" {
		return "打开视图（只读）", "查看视图结构（只读）"
	}
	return "打开表（只读）", "设计表（只读）"
}

// mysqlObjectWorkspace lists real INFORMATION_SCHEMA objects and explicitly opens
// read-only data or structure pages on the managed session.
func (s *State) mysqlObjectWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	db := s.mysqlSelectedDatabase
	items := s.mysqlSelectedObjects()
	connected := s.mysqlSessionStateFor(s.selectedProfileID) == "已连接"
	if !connected || db == "" {
		items = nil
	}
	message := ""
	if connected && db != "" {
		if s.mysqlObjectPending == db {
			message = "正在加载表和视图…"
		} else if err := s.mysqlObjectErrors[db]; err != "" {
			message = "读取对象失败：" + err
		} else if len(items) == 0 {
			message = "当前筛选下没有可见的表或视图"
		}
	}
	s.objectState.Selected = &s.objectRow
	s.objectState.Key = func(row int) any {
		if row < 0 || row >= len(items) {
			return nil
		}
		obj := items[row]
		return (navigationNode{Kind: "object", ProfileID: s.mysqlSessionProfileID, Database: db, Category: obj.Kind, Object: obj.Name}).key()
	}
	// Double-click opens real table/view data, as observed in the original.
	// Design remains a separate explicit read-only action.
	s.objectState.CellMenu = func(m *ui.Menu, row, col int) {
		if row < 0 || row >= len(items) {
			return
		}
		s.objectRow = row
		s.selectObjectInNavigator(db, items[row].Name, items[row].Kind)
		openLabel, designLabel := mysqlObjectActionLabels(items[row].Kind)
		if m.Item(locales.Text(openLabel)).Shortcut(0, ui.KeyEnter).Chosen() {
			s.openMySQLData(c.Services(), db, items[row].Name)
		}
		if m.Item(locales.Text(designLabel)).Shortcut(ui.Ctrl, ui.KeyD).Chosen() {
			s.openMySQLStructure(c.Services(), db, items[row].Name)
		}
	}
	columns := append([]ui.TableColumn(nil), mysqlObjectColumns...)
	for i := range columns {
		columns[i].ID = mysqlObjectColumns[i].Title
		columns[i].Title = locales.Text(columns[i].Title)
	}
	if len(items) == 0 {
		emptyTableView(c, &s.objectState, columns, message)
		if connected && db != "" && s.mysqlObjectErrors[db] != "" && ui.Button(c, locales.Text("重试")).Clicked() {
			s.loadMySQLObjects(c.Services(), db, true)
		}
		if connected && db != "" && s.mysqlObjectPending == db && ui.Button(c, locales.Text("取消加载")).Clicked() {
			s.cancelMySQLObjectQuery()
			s.mysqlObjectErrors[db] = "加载已取消"
		}
		return
	}
	previousRow := s.objectRow
	table := theme.Table(c, &s.objectState, columns, len(items), func(row, col int) {
		obj := items[row]
		switch col {
		case 0:
			iconName := "table"
			if obj.Kind == "视图" {
				iconName = "view"
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
		if s.objectRow >= 0 && s.objectRow < len(items) {
			s.openMySQLData(c.Services(), db, items[s.objectRow].Name)
		}
	}
	if s.objectRow >= 0 && s.objectRow < len(items) {
		if previousRow != s.objectRow || table.Changed() || (table.Focused() && parseNavigationNode(s.explorerName).Kind != "object") {
			s.selectObjectInNavigator(db, items[s.objectRow].Name, items[s.objectRow].Kind)
		}
		if table.Shortcut(ui.Ctrl, ui.KeyD) {
			s.openMySQLStructure(c.Services(), db, items[s.objectRow].Name)
		}
	}

}
