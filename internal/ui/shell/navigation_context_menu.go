package shell

import (
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

// navigationContextMenu targets the same row whether the pointer lands on
// its disclosure gutter, icon, text, or trailing empty space. The native
// Outline owns the row-wide hit area; item content keeps the same handler.
func (s *State) navigationContextMenu(services ui.Services, key string, m *ui.Menu) {
	if isGroupNode(key) {
		s.explorerMenuBuilt = true
		s.profileGroupMenu(m, groupFromNode(key))
		return
	}
	n := parseNavigationNode(key)
	if n.Kind == "connection" {
		for _, p := range s.profileEntries {
			if p.ID == n.ProfileID {
				s.explorerMenuBuilt = true
				s.profileConnectionMenu(m, p.Name, services)
				return
			}
		}
		return
	}
	if n.Kind != "database" && n.Kind != "category" && n.Kind != "object" {
		return
	}
	s.explorerMenuBuilt = true
	s.selectNavigationNode(services, key)
	s.explorerRow = -1
	if n.Kind == "database" {
		if m.Item(locales.Text("打开数据库")).Shortcut(0, ui.KeyEnter).
			Disabled(s.mysqlSessionStateFor(n.ProfileID) != "已连接").Chosen() {
			s.openMySQLDatabase(services, n.Database, false)
		}
		m.Separator()
	}
	if n.Kind == "database" || n.Kind == "category" && n.Category == "表" {
		if m.Item(locales.Text("新建表")).Shortcut(ui.Ctrl, ui.KeyN).
			Disabled(!s.canChangeMySQLTable(n.ProfileID)).Chosen() {
			s.beginMySQLTableChange(n.ProfileID, n.Database, "", mysqlsvc.MySQLTableCreate)
		}
		m.Separator()
	}
	switch n.Kind {
	case "object":
		s.mysqlObjectContextMenu(services, m, n.ProfileID, n.Database, n.Object, n.Category)
		return
	case "category":
		if m.Item(locales.Text("显示全部对象")).Disabled(!supportedMySQLCategory(n.Category)).Chosen() {
			s.filterMySQLObjects("全部")
		}
		m.Separator()
	}
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(s.mysqlObjectPending != "" || s.mysqlSessionStatus != "已连接").Chosen() {
		s.openMySQLDatabase(services, n.Database, true)
	}
}
