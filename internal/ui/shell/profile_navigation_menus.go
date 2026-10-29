package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

// The navigation root and its settings button share the same menu.
// Navicat's unused tree background does not display a context menu.
func (s *State) profileRootMenu(m *ui.Menu) {
	if m.Item(locales.Text("新建连接…")).Chosen() {
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("关闭所有连接")).Disabled(!s.sessions.HasSessions()).Chosen() {
		s.closeAllMySQLConnections()
	}
	if m.Item(locales.Text("管理连接…")).Chosen() {
		s.openConnectionManagement()
	}
	m.Separator()
	if m.Item(locales.Text("新建组")).Chosen() {
		s.navigation.BeginGroupEdit("")
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		s.loadProfiles()
	}
}

func (s *State) sidebarSettingsMenu(m *ui.Menu) {
	s.navigation.Group, s.navigation.Key = "", ""
	s.selectedProfileID = 0
	s.navigation.Row, s.sessions.Current().Row = -1, -1
	s.sessions.Current().Database = ""
	s.profileRootMenu(m)
}

func (s *State) profileGroupMenu(m *ui.Menu, group string) {
	s.selectGroup(group) // Right-click targets and highlights its own row.
	if m.Item(locales.Text("新建连接…")).Chosen() {
		s.selectGroup(group)
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("新建组")).Chosen() {
		s.navigation.BeginGroupEdit("")
	}
	if m.Item(locales.Text("删除组")).Chosen() {
		s.navigation.GroupPendingDelete = group
		s.navigation.GroupDeleteConfirm = true
	}
	if m.Item(locales.Text("重命名")).Shortcut(0, ui.KeyF2).Chosen() {
		s.navigation.BeginGroupEdit(group)
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		s.loadProfiles()
	}
}

func (s *State) profileConnectionMenu(m *ui.Menu, name string, services ui.Services) {
	var profile connections.Profile
	found := false
	for _, p := range s.navigation.Profiles() {
		if p.Name == name {
			profile = p
			found = true
			break
		}
	}
	if !found {
		return
	}
	s.selectNavigationNode(services, navigation.ConnectionKey(profile.ID))
	canMySQL := profile.Kind == connections.MySQL
	status := s.sessions.StatusFor(profile.ID)
	connected := status == "已连接"
	connecting := status == "连接中"
	connectLabel := "打开连接"
	if connected {
		connectLabel = "关闭连接"
	}
	if m.Item(locales.Text(connectLabel)).Shortcut(0, ui.KeyEnter).Disabled(!canMySQL || connecting).Chosen() {
		s.selectProfileByName(name)
		if connected {
			s.disconnectMySQLSession()
		} else {
			s.startMySQLSession(services)
		}
	}
	canSwitch := canMySQL && !connected && !connecting
	m.Submenu(locales.Text("切换连接配置文件"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("默认")).Checked(profile.ActiveVariant == "").Chosen() {
			s.setProfileActiveVariant(profile.ID, "")
		}
		for _, variant := range profile.Variants {
			if sub.Item(variant.Name).Checked(profile.ActiveVariant == variant.Name).Chosen() {
				s.setProfileActiveVariant(profile.ID, variant.Name)
			}
		}
	}).Disabled(!canSwitch || len(profile.Variants) == 0)
	m.Separator()
	if m.Item(locales.Text("编辑连接")).Disabled(connecting).Chosen() {
		s.selectProfileByName(name)
		s.startEditProfile()
	}
	if m.Item(locales.Text("新建连接")).Chosen() {
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("删除连接")).Shortcut(0, ui.KeyDelete).Disabled(connecting).Chosen() {
		s.selectProfileByName(name)
		s.confirmDeleteProfile()
	}
	if m.Item(locales.Text("复制连接")).Chosen() {
		s.selectProfileByName(name)
		s.duplicateProfile()
	}
	m.Separator()
	m.Item(locales.Text("新建数据库")).Disabled(true)
	if m.Item(locales.Text("新建查询")).Disabled(!connected).Chosen() {
		s.selectProfileByName(name)
		s.openQuery()
	}
	m.Separator()
	m.Item(locales.Text("命令列界面")).Disabled(true)
	m.Item(locales.Text("运行 SQL 文件…")).Disabled(true)
	m.Submenu(locales.Text("重载"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("刷新数据库对象")).Chosen() {
			s.refreshConnectionObjects(services, profile.ID)
		}
	}).Disabled(!connected)
	m.Separator()
	starLabel := "添加星标"
	if profile.Starred {
		starLabel = "移除星标"
	}
	if m.Item(locales.Text(starLabel)).Chosen() {
		s.setProfileStarred(profile.ID, !profile.Starred)
	}
	m.Submenu(locales.Text("颜色"), func(sub *ui.Menu) {
		sub.Item(locales.Text("默认颜色")).Disabled(true)
	}).Disabled(true)
	m.Separator()
	m.Submenu(locales.Text("管理组"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("新建组")).Chosen() {
			s.navigation.BeginGroupEdit("")
		}
		sub.Submenu(locales.Text("移动到"), func(dest *ui.Menu) {
			if dest.Item(locales.Text("未分组")).Checked(profile.GroupName == "").Chosen() {
				s.moveProfileToGroup(profile.ID, "")
			}
			for _, g := range s.navigation.Groups() {
				if dest.Item(g.Name).Checked(profile.GroupName == g.Name).Chosen() {
					s.moveProfileToGroup(profile.ID, g.Name)
				}
			}
		}).Disabled(len(s.navigation.Groups()) == 0)
		if sub.Item(locales.Text("从组中排除")).Disabled(profile.GroupName == "").Chosen() {
			s.moveProfileToGroup(profile.ID, "")
		}
	})
	m.Item(locales.Text("共享…")).Disabled(true)
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		if connected {
			s.sessions.RefreshDatabases(services.Invalidate)
		} else {
			s.loadProfiles()
		}
	}
}

func (s *State) navigationContextMenu(services ui.Services, key string, m *ui.Menu) {
	if navigation.IsGroup(key) {
		s.profileGroupMenu(m, navigation.GroupName(key))
		return
	}
	n := navigation.Parse(key)
	if n.Kind == "connection" {
		for _, p := range s.navigation.Profiles() {
			if p.ID == n.ProfileID {
				s.profileConnectionMenu(m, p.Name, services)
				return
			}
		}
		return
	}
	if n.Kind != "database" && n.Kind != "category" && n.Kind != "object" {
		return
	}
	s.selectNavigationNode(services, key)
	s.navigation.Row = -1
	if n.Kind == "database" {
		if m.Item(locales.Text("打开数据库")).Shortcut(0, ui.KeyEnter).
			Disabled(s.sessions.StatusFor(n.ProfileID) != "已连接").Chosen() {
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
		if m.Item(locales.Text("显示全部对象")).Disabled(!navigation.SupportedCategory(n.Category)).Chosen() {
			s.filterMySQLObjects("全部")
		}
		m.Separator()
	}
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(s.sessions.Current().Pending != "" || s.sessions.Current().Status != "已连接").Chosen() {
		s.openMySQLDatabase(services, n.Database, true)
	}
}
