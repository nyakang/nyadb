package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The navigation root and its settings button share the same menu.
// Navicat's unused tree background does not display a context menu.
func (s *State) profileRootMenu(m *ui.Menu) {
	if m.Item(locales.Text("新建连接…")).Chosen() {
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("关闭所有连接")).Disabled(s.mysqlSessionProfileID == 0).Chosen() {
		s.disconnectMySQLSession()
	}
	m.Item(locales.Text("管理连接…")).Disabled(true)
	m.Separator()
	if m.Item(locales.Text("新建组")).Chosen() {
		s.beginGroupEdit("")
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		s.loadProfiles()
	}
}

func (s *State) sidebarSettingsMenu(m *ui.Menu) {
	s.selectedGroup, s.explorerName = "", ""
	s.selectedProfileID = 0
	s.explorerRow, s.objectRow = -1, -1
	s.mysqlSelectedDatabase = ""
	s.profileRootMenu(m)
}

func (s *State) profileGroupMenu(m *ui.Menu, group string) {
	s.selectGroup(group) // Right-click targets and highlights its own row.
	if m.Item(locales.Text("新建连接…")).Chosen() {
		s.selectGroup(group)
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("新建组")).Chosen() {
		s.beginGroupEdit("")
	}
	if m.Item(locales.Text("删除组")).Chosen() {
		s.groupPendingDelete = group
		s.groupDeleteConfirm = true
	}
	if m.Item(locales.Text("重命名")).Shortcut(0, ui.KeyF2).Chosen() {
		s.beginGroupEdit(group)
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		s.loadProfiles()
	}
}

func (s *State) profileConnectionMenu(m *ui.Menu, name string, services ui.Services) {
	var profile connections.Profile
	found := false
	for _, p := range s.profileEntries {
		if p.Name == name {
			profile = p
			found = true
			break
		}
	}
	if !found {
		return
	}
	s.selectNavigationNode(services, profileNode(profile.ID))
	canMySQL := profile.Kind == connections.MySQL
	connected := s.mysqlSessionStateFor(profile.ID) == "已连接"
	connectLabel := "打开连接"
	if connected {
		connectLabel = "关闭连接"
	}
	if m.Item(locales.Text(connectLabel)).Shortcut(0, ui.KeyEnter).Disabled(!canMySQL).Chosen() {
		s.selectProfileByName(name)
		if connected {
			s.disconnectMySQLSession()
		} else {
			s.startMySQLSession(services)
		}
	}
	canSwitch := canMySQL && !connected && s.mysqlSessionStateFor(profile.ID) != "连接中"
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
	if m.Item(locales.Text("编辑连接")).Chosen() {
		s.selectProfileByName(name)
		s.startEditProfile()
	}
	if m.Item(locales.Text("新建连接")).Chosen() {
		s.openConnectionDialog()
	}
	if m.Item(locales.Text("删除连接")).Shortcut(0, ui.KeyDelete).Chosen() {
		s.selectProfileByName(name)
		s.confirmDeleteProfile()
	}
	if m.Item(locales.Text("复制连接")).Chosen() {
		s.selectProfileByName(name)
		s.duplicateProfile()
	}
	m.Separator()
	m.Item(locales.Text("新建数据库")).Disabled(true)
	m.Item(locales.Text("新建查询")).Disabled(true)
	m.Separator()
	m.Item(locales.Text("命令列界面")).Disabled(true)
	m.Item(locales.Text("运行 SQL 文件…")).Disabled(true)
	m.Submenu(locales.Text("重载"), func(sub *ui.Menu) {
		sub.Item(locales.Text("刷新数据库对象")).Disabled(true)
	}).Disabled(true)
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
			s.beginGroupEdit("")
		}
		sub.Submenu(locales.Text("移动到"), func(dest *ui.Menu) {
			if dest.Item(locales.Text("未分组")).Checked(profile.GroupName == "").Chosen() {
				s.moveProfileToGroup(profile.ID, "")
			}
			for _, g := range s.profileGroups {
				if dest.Item(g.Name).Checked(profile.GroupName == g.Name).Chosen() {
					s.moveProfileToGroup(profile.ID, g.Name)
				}
			}
		}).Disabled(len(s.profileGroups) == 0)
		if sub.Item(locales.Text("从组中排除")).Disabled(profile.GroupName == "").Chosen() {
			s.moveProfileToGroup(profile.ID, "")
		}
	})
	m.Item(locales.Text("共享…")).Disabled(true)
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
		if connected {
			s.refreshMySQLDatabases(services)
		} else {
			s.loadProfiles()
		}
	}
}

func (s *State) sidebarFilterMenu(m *ui.Menu) {
	if m.Item(locales.Text("所有连接类型")).Checked(s.profileEngine == "").Chosen() {
		s.profileEngine = ""
		s.changeProfileFilter()
	}
	for _, item := range []struct {
		label string
		kind  connections.Kind
	}{
		{"MySQL", connections.MySQL}, {"PostgreSQL", connections.PostgreSQL}, {"SQLite", connections.SQLite},
	} {
		if m.Item(item.label).Checked(s.profileEngine == item.kind).Chosen() {
			s.profileEngine = item.kind
			s.changeProfileFilter()
		}
	}
}

func (s *State) sidebarFooter(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).
		Padding(0, 4).Gap(0).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 24)).Gap(2).AlignItems(ui.Center).Children(func() {
			if ui.SearchField(c, &s.search).Label(locales.Text("搜索连接")).Placeholder(locales.Text("搜索")).Height(theme.RowHeight(c, 22)).Grow(1).MinWidth(0).Changed() {
				s.changeProfileFilter()
			}
			filter := ui.ButtonBase(c).Label(locales.Text("筛选连接")).Tooltip(locales.Text("筛选连接类型")).
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			filter.Children(func() { theme.Icon(c, "catalog-filter", 15, palette.Muted) })
			if s.profileEngine != "" {
				filter.Background(palette.Accent.Alpha(.28))
			}
			filter.Menu(func(m *ui.Menu) { s.sidebarFilterMenu(m) })
			starred := ui.ButtonBase(c).Label(locales.Text("显示已星标")).Tooltip(locales.Text("只显示已星标的连接")).
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			if s.profileStarredOnly {
				starred.Background(palette.Accent.Alpha(.28))
			}
			starred.Children(func() {
				color := palette.Muted
				if s.profileStarredOnly {
					color = palette.Gold
				}
				theme.Icon(c, "star", 15, color)
			})
			if starred.Clicked() {
				s.profileStarredOnly = !s.profileStarredOnly
				s.changeProfileFilter()
			}
			active := ui.ButtonBase(c).Label(locales.Text("导航至活动标签的对象")).
				Tooltip("在连接树中定位当前活动数据库对象").
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			_, _, _, canNavigate := s.activeTabObject()
			active.Disabled(!canNavigate)
			active.Children(func() { theme.Icon(c, "navigate-active", 15, palette.Muted) })
			if active.Clicked() {
				s.navigateToActiveTab(c.Services())
			}
			collapse := ui.ButtonBase(c).Label(locales.Text("全部折叠")).
				Tooltip("折叠所有连接组与数据库树").
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			collapse.Children(func() { theme.Icon(c, "collapse-all", 15, palette.Muted) })
			if collapse.Clicked() {
				s.collapseProfileNavigation()
			}
		})
	})
}
