package shell

import (
	"fmt"
	"iter"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// selectProfileByName activates the target profile without opening a session,
// including when a context menu targets a previously unselected row.
func (s *State) selectProfileByName(name string) {
	for _, p := range s.navigation.Profiles() {
		if p.Name != name {
			continue
		}
		// Keep background sessions alive, but show the selected connection's
		// own database browser instead of another profile's open data tab.
		if p.ID != s.selectedProfileID {
			if s.sessions.Current().ProfileID == 0 && s.sessions.Parked(p.ID) == nil {
				s.sessions.ResetInactive()
			}
			t := s.currentTab()
			if t.profileID() != 0 && t.profileID() != p.ID {
				s.activeTab = 0
			}
		}
		s.sessions.Activate(p.ID)
		if s.selectedProfileID != p.ID || s.navigation.Group != "" {
			s.selectedProfileID = p.ID
			s.navigation.Group = ""
			s.navigation.Key = navigation.ConnectionKey(p.ID)
			s.status = "已选择连接：" + name + "（" + s.sessions.StatusFor(p.ID) + "）"
		}
		s.navigation.Key = navigation.ConnectionKey(p.ID)
		for i := 0; i < s.navigation.Tree.Rows(); i++ {
			if s.navigation.Tree.Item(i) == navigation.ConnectionKey(p.ID) {
				s.navigation.Row = i
				break
			}
		}
		return
	}
}

func (s *State) inspectorHeader(c *ui.Context, title string) {
	palette := theme.Colors(c)

	ui.Row(c).Label(locales.Text(title)).Height(42).Padding(1, 8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(20)
		ui.Box(c).Grow(1)
		theme.Icon(c, "info", 16, palette.Blue)
		ui.Box(c).Grow(1)
		if ui.ButtonBase(c).Label(locales.Text("隐藏信息面板")).Tooltip(locales.Text("隐藏信息面板")).Size(20, 20).Children(func() { theme.Icon(c, "close", 12, palette.Muted) }).Clicked() {
			s.inspectorOn = false
		}
	})
}

func (s *State) profileInspector(c *ui.Context) {
	palette := theme.Colors(c)

	if s.currentTab().kind == tabMySQLData {
		s.dataWorkspace.InspectorView(c, s.currentTab().data)
		return
	}
	p, ok := s.currentProfile()
	ui.Column(c).Fill().Background(palette.Panel).Children(func() {
		s.inspectorHeader(c, "信息")
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(28, 14).Gap(16).Children(func() {
				if s.navigation.Group != "" {
					ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
						theme.Icon(c, "folder-filled", 50, palette.Gold)
						ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
							ui.Text(c, s.navigation.Group).FontSize(theme.Font(c, 14)).Bold()
							ui.Text(c, fmt.Sprintf("%s (%d %s)", locales.Text("虚拟组"), s.navigation.GroupCount(s.navigation.Group), locales.Text("对象"))).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
						})
					})
					return
				}
				t := s.currentTab()
				if t.kind == tabStructure && t.object.Data() != nil {
					s.objectWorkspace.InspectorView(c, t.object)
					return
				}
				if !ok {
					if s.selectedProfileID == 0 && s.navigation.Group == "" {
						ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
							theme.Icon(c, "connection", 48, palette.Muted)
							ui.Text(c, locales.Text("我的连接")).FontSize(theme.Font(c, 14)).Bold()
						})
						return
					}
					ui.Text(c, locales.Text("选择连接以查看档案信息")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					return
				}
				n := navigation.Parse(s.navigation.Key)
				if n.Database != "" && s.sessions.StatusFor(p.ID) == "已连接" {
					name, icon := n.Database, "database"
					if n.Kind == "object" {
						name, icon = n.Object, "table"
					}
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						theme.Icon(c, icon, 32, palette.Blue)
						ui.Text(c, name).FontSize(theme.Font(c, 14)).Bold()
					})
					theme.Property(c, locales.Text("类型"), s.navigationDescription(n))
					theme.Property(c, locales.Text("连接"), p.Name)
					theme.Property(c, locales.Text("数据库"), n.Database)
					for _, obj := range s.sessions.Current().Cache[n.Database] {
						if obj.Name == n.Object && obj.Kind == n.Category {
							objectworkspace.ObjectProperties(c, obj)
							break
						}
					}
					return
				}
				ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
					navigation.BrandIcon(c, p.Kind, 50)
					ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
						ui.Text(c, p.Name).FontSize(theme.Font(c, 14)).Bold()
						ui.Text(c, connectionKindName(p.Kind)).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					})
				})
				if p.Kind == connections.MySQL {
					s.mysqlSessionToolbar(c)
				}
				theme.Property(c, locales.Text("状态"), locales.Text(s.sessions.StatusFor(p.ID)))
				if p.Kind == connections.SQLite {
					theme.Property(c, locales.Text("文件"), p.Database)
					return
				}
				if p.Kind == connections.MySQL {
					resolved, err := connections.ResolveActiveSettings(p)
					if err != nil {
						theme.Property(c, locales.Text("活动配置"), locales.Text("无效，请编辑连接"))
						return
					}
					activeName := resolved.ConfigurationName
					if activeName == "" {
						activeName = locales.Text("主要配置文件")
					}
					theme.Property(c, locales.Text("活动配置"), activeName)
					theme.Property(c, locales.Text("主机"), resolved.Host)
					theme.Property(c, locales.Text("端口"), fmt.Sprint(resolved.Port))
					theme.Property(c, locales.Text("用户"), resolved.Username)
					theme.Property(c, locales.Text("数据库"), resolved.Database)
				} else {
					theme.Property(c, locales.Text("主机"), p.Host)
					theme.Property(c, locales.Text("端口"), fmt.Sprint(p.Port))
					theme.Property(c, locales.Text("用户"), p.Username)
					theme.Property(c, locales.Text("数据库"), p.Database)
				}
			})
		})
	})
}

// navigationDependencies keeps session and document actions in the workbench;
// the navigator owns presentation, filtering, tree selection and group drafts.
func (s *State) navigationDependencies() navigation.Dependencies {
	return navigation.Dependencies{
		SessionStatus:  s.sessions.StatusFor,
		Databases:      s.sessions.DatabasesFor,
		DatabaseOpened: s.sessions.DatabaseOpened,
		Objects: func(id int64, database string) (iter.Seq[navigation.Object], bool) {
			objects, loaded := s.sessions.CacheFor(id)[database]
			// Project names lazily so large catalogs do not get copied on each frame.
			return func(yield func(navigation.Object) bool) {
				for _, object := range objects {
					if !yield(navigation.Object{Name: object.Name, Kind: object.Kind}) {
						return
					}
				}
			}, loaded
		},
		LoadState: s.sessions.LoadStateFor,
		Select:    s.selectNavigationNode,
		Submit:    s.submitNavigation,
		OpenDatabase: func(services ui.Services, node navigation.Node, force bool) {
			s.openMySQLDatabase(services, node.Database, force)
		},
		ContextMenu:    s.navigationContextMenu,
		SettingsMenu:   s.sidebarSettingsMenu,
		Shortcut:       s.navigationShortcut,
		SyncExpansion:  s.syncNavigationExpansion,
		CanNavigate:    func() bool { _, _, _, ok := s.activeTabObject(); return ok },
		NavigateActive: s.navigateToActiveTab,

		GroupSelected: s.selectGroup,
		Changed:       s.loadProfiles,
		Status:        func(message string) { s.status = message },
	}
}

func (s *State) navigationShortcut(services ui.Services, n navigation.Node, shortcut navigation.Shortcut) {
	switch shortcut.Key {
	case ui.KeyF2:
		if n.Kind == "object" && n.Category == "表" {
			s.objectWorkspace.BeginTableChange(n.ProfileID, n.Database, n.Object, mysqlsvc.MySQLTableRename)
		} else if s.navigation.Group != "" {
			s.navigation.BeginGroupEdit(s.navigation.Group)
		}
	case ui.KeyDelete:
		if n.Kind == "object" && n.Category == "表" {
			s.objectWorkspace.BeginTableChange(n.ProfileID, n.Database, n.Object, mysqlsvc.MySQLTableDrop)
		} else if n.Kind == "connection" && s.selectedProfileID != 0 {
			s.confirmDeleteProfile()
		}
	case ui.KeyN:
		if n.Kind == "database" || (n.Kind == "category" || n.Kind == "object") && n.Category == "表" {
			s.objectWorkspace.BeginTableChange(n.ProfileID, n.Database, "", mysqlsvc.MySQLTableCreate)
		}
	case ui.KeyD:
		if n.Kind == "object" && !objectworkspace.Supports(n.Category) {
			s.openMySQLStructure(services, n.Database, n.Object)
		}
	case ui.KeyF5:
		if n.Database != "" {
			s.openMySQLDatabase(services, n.Database, true)
		} else if s.sessions.StatusFor(n.ProfileID) == "已连接" {
			s.sessions.RefreshDatabases(services.Invalidate)
		} else {
			s.loadProfiles()
		}
	}
}
