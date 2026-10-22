package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// These menus are independently designed against the visible navigation
// concepts. Menu command membership is not a claim about Navicat internals.
func (s *State) profileBlankMenu(m *ui.Menu) {
	if m.Item("新建连接…").Chosen() {
		s.openConnectionDialog()
	}
	if m.Item("新建连接组…").Chosen() {
		s.beginGroupEdit("")
	}
	m.Separator()
	if m.Item("展开所有连接组").Chosen() {
		for _, g := range s.profileGroups {
			s.explorerTree.Open.Add(groupNode(g.Name))
		}
		s.explorerRow = -1
	}
	if m.Item("全部折叠").Chosen() {
		s.collapseProfileNavigation()
	}
	if m.Item("刷新连接列表").Chosen() {
		s.loadProfiles()
	}
}

func (s *State) sidebarSettingsMenu(m *ui.Menu) {
	s.profileBlankMenu(m)
	m.Separator()
	if m.Item("只显示已星标").Checked(s.profileStarredOnly).Chosen() {
		s.profileStarredOnly = !s.profileStarredOnly
		s.changeProfileFilter()
	}
}

func (s *State) profileGroupMenu(m *ui.Menu, group string) {
	if m.Item("新建连接…").Chosen() {
		s.selectGroup(group)
		s.openConnectionDialog()
	}
	if m.Item("新建连接组…").Chosen() {
		s.beginGroupEdit("")
	}
	m.Separator()
	if m.Item("展开该组").Chosen() {
		s.explorerTree.Open.Add(groupNode(group))
		s.explorerRow = -1
	}
	if m.Item("折叠该组").Chosen() {
		s.explorerTree.Open.Remove(groupNode(group))
		s.explorerRow = -1
	}
	m.Separator()
	if m.Item("重命名组").Chosen() {
		s.beginGroupEdit(group)
	}
	if m.Item("删除连接组").Chosen() {
		s.groupPendingDelete = group
		s.groupDeleteConfirm = true
	}
	m.Separator()
	if m.Item("刷新连接列表").Chosen() {
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
	canMySQL := profile.Kind == connections.MySQL
	connected := s.mysqlSessionStateFor(profile.ID) == "已连接"
	if m.Item("连接").Disabled(!canMySQL || connected).Chosen() {
		s.selectProfileByName(name)
		s.startMySQLSession(services)
	}
	if m.Item("断开连接").Disabled(!connected).Chosen() {
		s.selectProfileByName(name)
		s.disconnectMySQLSession()
		s.status = "连接已断开"
	}
	m.Separator()
	if m.Item("编辑连接").Chosen() {
		s.selectProfileByName(name)
		s.startEditProfile()
	}
	if m.Item("复制连接").Chosen() {
		s.selectProfileByName(name)
		s.duplicateProfile()
	}
	if m.Item("添加星标").Checked(profile.Starred).Chosen() {
		s.setProfileStarred(profile.ID, !profile.Starred)
	}
	m.Submenu("移动到组", func(sub *ui.Menu) {
		if sub.Item("未分组").Chosen() {
			s.moveProfileToGroup(profile.ID, "")
		}
		for _, g := range s.profileGroups {
			if sub.Item(g.Name).Checked(profile.GroupName == g.Name).Chosen() {
				s.moveProfileToGroup(profile.ID, g.Name)
			}
		}
	})
	m.Separator()
	if m.Item("删除连接").Chosen() {
		s.selectProfileByName(name)
		s.confirmDeleteProfile()
	}
	if m.Item("刷新连接列表").Chosen() {
		s.loadProfiles()
	}
}

func (s *State) sidebarFilterMenu(m *ui.Menu) {
	if m.Item("所有连接类型").Checked(s.profileEngine == "").Chosen() {
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
	ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).
		Padding(2, 4).Gap(1).Children(func() {
		ui.Row(c).Height(28).Gap(2).AlignItems(ui.Center).Children(func() {
			if ui.SearchField(c, &s.search).Label("搜索连接").Placeholder("搜索").Grow(1).MinWidth(0).Changed() {
				s.changeProfileFilter()
			}
			filter := ui.ButtonBase(c).Label("筛选连接").Tooltip("筛选连接类型").
				Width(24).Height(24).Padding(3).Radius(1)
			filter.Children(func() { theme.Icon(c, "catalog-filter", 15, theme.Muted) })
			if s.profileEngine != "" {
				filter.Background(theme.Accent.Alpha(.28))
			}
			filter.Menu(func(m *ui.Menu) { s.sidebarFilterMenu(m) })
			starred := ui.ButtonBase(c).Label("显示已星标").Tooltip("只显示已星标的连接").
				Width(24).Height(24).Padding(3).Radius(1)
			if s.profileStarredOnly {
				starred.Background(theme.Accent.Alpha(.28))
			}
			starred.Children(func() {
				color := theme.Muted
				if s.profileStarredOnly {
					color = theme.Gold
				}
				theme.Icon(c, "star", 15, color)
			})
			if starred.Clicked() {
				s.profileStarredOnly = !s.profileStarredOnly
				s.changeProfileFilter()
			}
			active := ui.ButtonBase(c).Label("导航至活动标签的对象").
				Tooltip("在连接树中定位当前活动数据库对象").
				Width(24).Height(24).Padding(3).Radius(1)
			_, _, _, canNavigate := s.activeTabObject()
			active.Disabled(!canNavigate)
			active.Children(func() { theme.Icon(c, "navigate-active", 15, theme.Muted) })
			if active.Clicked() {
				s.navigateToActiveTab(c.Services())
			}
			collapse := ui.ButtonBase(c).Label("全部折叠").
				Tooltip("折叠所有连接组与数据库树").
				Width(24).Height(24).Padding(3).Radius(1)
			collapse.Children(func() { theme.Icon(c, "collapse-all", 15, theme.Muted) })
			if collapse.Clicked() {
				s.collapseProfileNavigation()
			}
		})
	})
}
