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

func (s *State) navigationChildren(key string) []string {
	n := parseNavigationNode(key)
	if s.mysqlSessionStateFor(n.ProfileID) != "已连接" {
		return nil
	}
	switch n.Kind {
	case "connection":
		children := make([]string, 0, len(s.mysqlSessionDatabases))
		for _, db := range s.mysqlSessionDatabases {
			children = append(children, databaseNode(n.ProfileID, db).key())
		}
		return children
	case "database":
		children := []string{}
		if _, loaded := s.mysqlObjectCache[n.Database]; !loaded {
			return children
		}
		for _, kind := range []string{"表", "视图"} {
			child := n
			child.Kind, child.Category = "category", kind
			children = append(children, child.key())
		}
		return children
	case "category":
		children := []string{}
		for _, obj := range s.mysqlObjectCache[n.Database] {
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
	if n.ProfileID == 0 {
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
	// Outline supplies the indentation and disclosure arrow. Fill the remaining
	// row so its context menu also receives clicks after a short name.
	if isGroupNode(key) {
		name := groupFromNode(key)
		ui.Row(c).Grow(1).MinWidth(0).Height(theme.TreeRowHeight - 2).AlignItems(ui.Center).Gap(5).Children(func() {
			theme.Icon(c, "folder-filled", 16, theme.Gold)
			ui.Text(c, name).FontSize(12).SingleLine()
		}).ContextMenu(func(m *ui.Menu) {
			s.profileGroupMenu(m, name)
			s.explorerMenuBuilt = true
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
			row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.TreeRowHeight - 2).AlignItems(ui.Center).Gap(5).Children(func() {
				s.profileBrandIcon(c, p.Kind, 16)
				ui.Text(c, p.Name).FontSize(12).SingleLine()
			})
			row.ContextMenu(func(m *ui.Menu) {
				s.profileConnectionMenu(m, p.Name, c.Services())
				s.explorerMenuBuilt = true
			})

		}
		return
	case "database":
		label, icon = n.Database, "database"
	case "category":
		label = locales.Text(n.Category)
	}
	if n.Category == "视图" {
		icon = "view"
	}
	row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.TreeRowHeight - 2).AlignItems(ui.Center).Gap(5).Children(func() {
		theme.Icon(c, icon, 16, theme.Blue)
		ui.Text(c, label).FontSize(12).SingleLine()
	})
	if n.Kind == "database" {
		row.Label("数据库：" + n.Database)
	}
	row.ContextMenu(func(m *ui.Menu) {
		s.explorerMenuBuilt = true
		s.selectNavigationNode(c.Services(), key)
		s.explorerRow = -1
		if n.Kind == "object" {
			if m.Item(locales.Text("打开表（只读）")).Shortcut(0, ui.KeyEnter).Chosen() {
				s.openMySQLData(c.Services(), n.Database, n.Object)
			}
			if m.Item(locales.Text("设计表（只读）")).Shortcut(ui.Ctrl, ui.KeyD).Chosen() {
				s.openMySQLStructure(c.Services(), n.Database, n.Object)
			}
			m.Separator()
		}
		if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
			s.loadMySQLObjects(c.Services(), n.Database, true)
		}
	})
	if row.Clicked() {
		s.selectNavigationNode(c.Services(), key)
	}

}

func (s *State) submitNavigation(c *ui.Context) {
	n := parseNavigationNode(s.explorerName)
	if n.Kind == "object" {
		s.openMySQLData(c.Services(), n.Database, n.Object)
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
		return fmt.Sprintf("%s (%d)", locales.Text(n.Category), len(s.mysqlSelectedObjects()))
	}
	return locales.Text(n.Category)
}

func (s *State) selectObjectInNavigator(database, name, kind string) {
	n := navigationNode{Kind: "object", ProfileID: s.mysqlSessionProfileID, Database: database, Category: kind, Object: name}
	s.revealNavigationNode(n)
}
