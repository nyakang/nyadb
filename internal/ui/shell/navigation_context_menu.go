package shell

import (
	"github.com/egoist/mygo/ui"
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
	switch n.Kind {
	case "object":
		openLabel, structureLabel := mysqlObjectActionLabels(n.Category)
		if m.Item(locales.Text(openLabel)).Shortcut(0, ui.KeyEnter).Chosen() {
			s.openMySQLObject(services, n.Database, n.Object, n.Category)
		}
		if m.Item(locales.Text(structureLabel)).Shortcut(ui.Ctrl, ui.KeyD).Disabled(isMySQLProgram(n.Category)).Chosen() {
			s.openMySQLStructure(services, n.Database, n.Object)
		}
		m.Separator()
	case "category":
		if m.Item(locales.Text("显示全部对象")).Disabled(!supportedMySQLCategory(n.Category)).Chosen() {
			s.filterMySQLObjects("全部")
		}
		m.Separator()
	}
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(s.mysqlObjectPending != "" || s.mysqlSessionStatus != "已连接").Chosen() {
		s.loadMySQLObjects(services, n.Database, true)
	}
}
