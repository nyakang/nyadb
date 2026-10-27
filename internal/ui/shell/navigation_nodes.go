package shell

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Keys are independent of display labels: the same table name in two databases
// must never share selection, expansion, or a context-menu target.
type navigationNode struct {
	Kind      string
	ProfileID int64
	Database  string
	Category  string
	Object    string
}

const navigationPrefix = "\x1fnavigation:"

func (n navigationNode) key() string {
	b, _ := json.Marshal(n)
	return navigationPrefix + string(b)
}

func profileNode(id int64) string { return (navigationNode{Kind: "connection", ProfileID: id}).key() }
func databaseNode(id int64, database string) navigationNode {
	return navigationNode{Kind: "database", ProfileID: id, Database: database}
}

func parseNavigationNode(key string) navigationNode {
	var n navigationNode
	if strings.HasPrefix(key, navigationPrefix) {
		_ = json.Unmarshal([]byte(strings.TrimPrefix(key, navigationPrefix)), &n)
	}
	return n
}

// Database programs are real server metadata; queries and backups remain
// workspace features until they have their own persistence implementations.
var mysqlNavigationCategories = []string{"表", "视图", "函数", "存储过程", "事件", "查询", "备份"}

func supportedMySQLCategory(category string) bool {
	switch category {
	case "表", "视图", "函数", "存储过程", "事件":
		return true
	}
	return false
}

func (s *State) navigationChildren(key string) []string {
	n := parseNavigationNode(key)
	if s.mysqlSessionStateFor(n.ProfileID) != "已连接" {
		return nil
	}
	switch n.Kind {
	case "connection":
		databases := s.mysqlSessionDatabasesFor(n.ProfileID)
		children := make([]string, 0, len(databases))
		for _, db := range databases {
			children = append(children, databaseNode(n.ProfileID, db).key())
		}
		return children
	case "database":
		if _, loaded := s.mysqlObjectCacheFor(n.ProfileID)[n.Database]; !loaded {
			status := n
			status.Kind, status.Object = "status", "waiting"
			pending, err := s.mysqlObjectLoadStateFor(n.ProfileID, n.Database)
			if pending {
				status.Object = "loading"
			} else if err != "" {
				status.Object = "error"
			}
			return []string{status.key()}
		}
		children := make([]string, 0, len(mysqlNavigationCategories))
		for _, kind := range mysqlNavigationCategories {
			child := n
			child.Kind, child.Category = "category", kind
			children = append(children, child.key())
		}
		return children
	case "category":
		if !supportedMySQLCategory(n.Category) {
			return nil
		}
		children := []string{}
		for _, obj := range s.mysqlObjectCacheFor(n.ProfileID)[n.Database] {
			if obj.Kind == n.Category {
				child := n
				child.Kind, child.Object = "object", obj.Name
				children = append(children, child.key())
			}
		}
		return children
	}
	return nil
}

func (s *State) selectNavigationNode(services ui.Services, key string) {
	if isGroupNode(key) {
		s.selectGroup(groupFromNode(key))
		return
	}
	n := parseNavigationNode(key)
	if n.ProfileID == 0 || n.Kind == "status" {
		return
	}
	for _, p := range s.profileEntries {
		if p.ID == n.ProfileID {
			s.selectProfileByName(p.Name)
			break
		}
	}
	s.explorerName = key
	s.explorerRow = s.outlineRowForSelection(s.profileRoots())
	if n.Database == "" {
		s.cancelMySQLObjectQuery()
		s.mysqlSelectedDatabase = ""
		s.mysqlObjectKind = "全部"
		s.objectRow = -1
		return
	}
	if s.mysqlSessionStateFor(n.ProfileID) != "已连接" {
		return
	}
	s.selectMySQLDatabase(services, n.Database)
	s.mysqlObjectKind = "全部"
	if n.Category != "" {
		s.mysqlObjectKind = n.Category
	}
	s.objectRow = -1
	if n.Kind == "category" && !supportedMySQLCategory(n.Category) {
		s.status = n.Category + "：此类对象尚未提供元数据浏览"
		return
	}
	if len(s.mysqlSelectedObjects()) > 0 {
		s.objectRow = 0
	}
	if n.Kind == "object" {
		for i, obj := range s.mysqlSelectedObjects() {
			if obj.Name == n.Object {
				s.objectRow = i
				break
			}
		}
	}
}

func (s *State) revealNavigationNode(n navigationNode) {
	s.explorerTree.Open.Add(profileNode(n.ProfileID))
	if n.Database != "" {
		s.explorerTree.Open.Add(databaseNode(n.ProfileID, n.Database).key())
	}
	if n.Category != "" {
		category := n
		category.Kind, category.Object = "category", ""
		s.explorerTree.Open.Add(category.key())
	}
	s.explorerName, s.explorerRow = n.key(), -1
}

// Opening a database is the only tree expansion that schedules metadata. A
// failed read stays failed until an explicit retry, avoiding frame-driven loops.
func (s *State) syncNavigationExpansion(services ui.Services) {
	if s.mysqlSessionStatus != "已连接" {
		return
	}
	if s.mysqlDatabaseNodes == nil {
		s.mysqlDatabaseNodes = make(map[string]*mysqlDatabaseNode)
	}
	for _, db := range s.mysqlSessionDatabases {
		node := s.mysqlDatabaseNodes[db]
		if node == nil {
			node = &mysqlDatabaseNode{}
			s.mysqlDatabaseNodes[db] = node
		}
		open := s.explorerTree.Open.Has(profileNode(s.mysqlSessionProfileID)) && s.explorerTree.Open.Has(databaseNode(s.mysqlSessionProfileID, db).key())
		node.open = open
		if !open || s.mysqlObjectPending != "" || s.mysqlObjectErrors[db] != "" {
			continue
		}
		if _, loaded := s.mysqlObjectCache[db]; !loaded {
			// The service has one outstanding metadata request. Drain open nodes in
			// order without changing selection or cancelling a sibling's read.
			s.loadMySQLObjects(services, db, false)
		}
	}
}

func (s *State) navigationRow(c *ui.Context, key string) {
	palette := theme.Colors(c)

	// Outline supplies the indentation and disclosure arrow. Fill the remaining
	// row so its context menu also receives clicks after a short name.
	if isGroupNode(key) {
		name := groupFromNode(key)
		if s.groupEditing && s.groupEditOriginal == name {
			ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, theme.TreeRowHeight+3)).
				Padding(0, 13).AlignItems(ui.Center).Gap(2).Children(func() {
				theme.Icon(c, "folder-filled", 16, palette.Gold)
				entry := ui.TextInput(c, &s.groupDraft).Label(locales.Text("连接组名称")).
					Height(theme.RowHeight(c, 20)).Grow(1).MinWidth(35).AutoFocus()
				if entry.Submitted() {
					s.saveGroup()
				}
				if ui.ButtonBase(c).Label(locales.Text("保存组")).Tooltip(locales.Text("保存组")).
					Size(20, 20).Padding(2).Children(func() {
					theme.Icon(c, "check", 14, palette.Success)
				}).Clicked() {
					s.saveGroup()
				}
				if ui.ButtonBase(c).Label(locales.Text("取消编辑组")).Tooltip(locales.Text("取消编辑组")).
					Size(20, 20).Padding(2).Children(func() {
					theme.Icon(c, "close", 14, palette.Muted)
				}).Clicked() {
					s.cancelGroupEdit()
				}
			})
			return
		}
		ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, theme.TreeRowHeight-2)).Padding(0, 8).AlignItems(ui.Center).Gap(4).Children(func() {
			theme.Icon(c, "folder-filled", 16, palette.Gold)
			ui.Text(c, name).FontSize(theme.Font(c, 12)).TextColor(palette.Text).SingleLine()
		}).ContextMenu(func(m *ui.Menu) {
			s.navigationContextMenu(c.Services(), key, m)
		})
		return
	}
	n := parseNavigationNode(key)
	label, icon := n.Object, "table"
	switch n.Kind {
	case "connection":
		for _, p := range s.profileEntries {
			if p.ID != n.ProfileID {
				continue
			}
			row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, theme.TreeRowHeight-2)).Padding(0, 6).AlignItems(ui.Center).Gap(4).Children(func() {
				status := s.mysqlSessionStateFor(p.ID)
				s.profileNavigationIcon(c, p.Kind, status, 16)
				ui.Text(c, p.Name).FontSize(theme.Font(c, 12)).TextColor(palette.Text).SingleLine()
			})
			row.Label(p.Name + " · " + locales.Text(s.mysqlSessionStateFor(p.ID)))
			row.ContextMenu(func(m *ui.Menu) {
				s.navigationContextMenu(c.Services(), key, m)
			})

		}
		return
	case "database":
		label, icon = n.Database, "database-solid"
	case "category":
		label = locales.Text(n.Category)
		switch n.Category {
		case "函数":
			icon = "function"
		case "存储过程":
			icon = "function"
		case "事件":
			icon = "refresh"
		case "查询":
			icon = "query"
		case "备份":
			icon = "backup"
		}
	case "status":
		icon, label = "refresh", locales.Text("等待加载")
		if n.Object == "loading" {
			label = locales.Text("正在读取对象…")
		} else if n.Object == "error" {
			label = locales.Text("读取失败，点击重试")
		}
	}
	if n.Category == "视图" {
		icon = "view"
	}
	inset := float32(5)
	row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, theme.TreeRowHeight-2)).Padding(0, inset).AlignItems(ui.Center).Gap(4).Children(func() {
		ink := palette.Text
		iconInk := palette.Text.Mix(palette.Blue, .22)
		if n.Kind == "category" && supportedMySQLCategory(n.Category) || n.Kind == "object" {
			// Database object categories use a soft functional blue, rather
			// than the gray used for the unimplemented query/backup nodes.
			iconInk = palette.Blue.Mix(palette.Muted, .17)
		}
		if n.Kind == "database" {
			// Neutral cylinders and a colored current-database indicator.
			iconInk = palette.Muted.Mix(palette.Text, .11)
			if s.explorerName == key {
				iconInk = palette.Success.Mix(palette.Sidebar, .23)
			}
		}
		if n.Kind == "status" || n.Kind == "category" && !supportedMySQLCategory(n.Category) {
			ink = palette.Muted
			iconInk = palette.Muted
		}
		if n.Kind == "status" && n.Object == "error" {
			ink = palette.Error
			iconInk = palette.Error
		}
		theme.Icon(c, icon, 15, iconInk)
		ui.Text(c, label).FontSize(theme.Font(c, 12)).TextColor(ink).SingleLine()
	})
	if n.Kind == "category" && !supportedMySQLCategory(n.Category) {
		row.Label(label + " · " + locales.Text("尚未支持元数据浏览"))
	}
	if n.Kind == "status" {
		if n.Object == "error" {
			_, err := s.mysqlObjectLoadStateFor(n.ProfileID, n.Database)
			row.Label(label + "：" + err)
		}
		if row.Clicked() && n.Object == "error" {
			s.loadMySQLObjects(c.Services(), n.Database, true)
		}
		return
	}
	if n.Kind == "database" {
		row.Label("数据库：" + n.Database)
	}
	row.ContextMenu(func(m *ui.Menu) {
		s.navigationContextMenu(c.Services(), key, m)
	})
	if row.Clicked() {
		s.selectNavigationNode(c.Services(), key)
	}

}

func (s *State) submitNavigation(c *ui.Context) {
	n := parseNavigationNode(s.explorerName)
	if n.Kind == "object" {
		s.openMySQLObject(c.Services(), n.Database, n.Object, n.Category)
	}
	if n.Kind == "connection" {
		if s.mysqlSessionStateFor(n.ProfileID) != "已连接" {
			s.startMySQLSession(c.Services())
		} else {
			s.explorerTree.Open.Add(n.key())
		}
	}
}

func (s *State) navigationDescription(n navigationNode) string {
	if n.Kind == "database" {
		return locales.Text("数据库")
	}
	if n.Kind == "category" {
		if !supportedMySQLCategory(n.Category) {
			return locales.Text(n.Category) + "（尚未支持）"
		}
		return fmt.Sprintf("%s (%d)", locales.Text(n.Category), len(s.mysqlSelectedObjects()))
	}
	return locales.Text(n.Category)
}

func (s *State) selectObjectInNavigator(database, name, kind string) {
	n := navigationNode{Kind: "object", ProfileID: s.mysqlSessionProfileID, Database: database, Category: kind, Object: name}
	s.revealNavigationNode(n)
}
