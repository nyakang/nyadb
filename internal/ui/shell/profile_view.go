package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) profileSidebar(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.Sidebar).Children(func() {
		header := ui.Row(c).Height(theme.RowHeight(c, 22)).Padding(0, 17).AlignItems(ui.Center).Background(palette.Sidebar).Gap(6)
		header.Children(func() {
			theme.Icon(c, "connection", 15, palette.Muted)
			ui.Text(c, locales.Text("我的连接")).FontSize(theme.Font(c, assetFontSize))
			ui.Box(c).Grow(1)
			if header.Hovered() {
				header.Background(palette.Selection)
				header.TextColor(palette.SelectionText)
				settings := ui.ButtonBase(c).Label(locales.Text("连接面板设置")).Tooltip(locales.Text("连接面板设置")).Padding(3, 4).Radius(2)
				settings.Children(func() { theme.Icon(c, "settings", 15, palette.Muted) })
				settings.Menu(func(m *ui.Menu) { s.sidebarSettingsMenu(m) })
			}
		})
		header.ContextMenu(func(m *ui.Menu) { s.sidebarSettingsMenu(m) })
		// New groups have no tree row yet; existing groups edit in place.
		if s.groupEditing && s.groupEditOriginal == "" {
			ui.Row(c).Padding(3, 6).Gap(4).AlignItems(ui.Center).Children(func() {
				entry := ui.TextInput(c, &s.groupDraft).Label(locales.Text("连接组名称")).Placeholder(locales.Text("组名称")).Grow(1).AutoFocus()
				if entry.Submitted() {
					s.saveGroup()
				}
				if ui.Button(c, locales.Text("保存")).Label(locales.Text("保存组")).Clicked() {
					s.saveGroup()
				}
				if ui.Button(c, "×").Label(locales.Text("取消编辑组")).Clicked() {
					s.cancelGroupEdit()
				}
			})
		}
		if s.groupEditing && s.groupError != "" {
			ui.Text(c, s.groupError).Padding(2, 8).TextColor(palette.Error).FontSize(theme.Font(c, 11))
		}
		navigator := ui.Column(c).Grow(1).MinHeight(0).Gap(0)
		navigator.Children(func() {
			roots := s.profileRoots()
			if len(roots) == 0 {
				msg := "暂无连接，点击工具栏“连接”新建连接"
				if strings.TrimSpace(s.search) != "" || s.profileStarredOnly || s.profileEngine != "" {
					msg = "没有匹配的连接或组"
				}
				ui.Text(c, locales.Text(msg)).Padding(8).TextColor(palette.Muted).FontSize(theme.Font(c, 11))
				return
			}
			selectedKey := s.explorerName
			if s.explorerRow < 0 {
				s.explorerRow = s.outlineRowForSelection(roots)
			} else if s.explorerRow < s.explorerTree.Rows() {
				selectedKey = s.explorerTree.Item(s.explorerRow)
			}
			// MyGo selects a right-clicked row before building its popup only
			// with Selection. Keep this navigator's choice singular, using the
			// native cursor for both pointer and keyboard navigation.
			s.explorerSelection.Clear()
			if selectedKey != "" {
				s.explorerSelection.Add(selectedKey)
			}
			s.explorerTree.List.Selection = &s.explorerSelection
			s.explorerTree.List.Selected = &s.explorerRow
			s.explorerTree.Indent = 14       // native disclosure steps: group -> connection -> database
			s.explorerTree.LeadingInset = 14 // reserve the same left tree gutter at every depth
			s.explorerTree.RowContextMenu = func(key string, menu *ui.Menu) {
				s.navigationContextMenu(c.Services(), key, menu)
			}
			savedTheme := c.Theme()
			denseTheme := *savedTheme
			// The native triangle has its own 10-DIP hit target; no need to
			// inflate spacing for its legibility.
			denseTheme.Spacing = 1
			// Collapsed nodes carry muted, secondary disclosure marks. Keep
			// the branch click target, but avoid competing with the database
			// cylinder or the selection highlight.
			denseTheme.TextMuted = palette.Muted.Mix(palette.Sidebar, .42)
			configureNavigatorDisclosure(&s.explorerTree)
			c.SetTheme(&denseTheme)
			tree := theme.NavigationOutline(c, &s.explorerTree, roots, s.profileChildren, func(key string) { s.navigationRow(c, key) }).Grow(1).Padding(0).Gap(0)
			c.SetTheme(savedTheme)
			if s.explorerMenuBuilt {
				// Outline can finish applying an earlier pointer selection after
				// the row's menu callback. Keep the menu's identity authoritative
				// and resolve its row on the next pass instead of restoring that
				// earlier click as the menu target.
				s.explorerRow = -1
				s.explorerMenuBuilt = false
			} else if s.explorerRow >= 0 && s.explorerRow < s.explorerTree.Rows() {
				key := s.explorerTree.Item(s.explorerRow)
				if key != s.explorerName {
					s.selectNavigationNode(c.Services(), key)
				}
			}
			if tree.Submitted() {
				s.submitNavigation(c)
			}
			if tree.Shortcut(0, ui.KeyF2) {
				n := parseNavigationNode(s.explorerName)
				if n.Kind == "object" && n.Category == "表" {
					s.beginMySQLTableChange(n.ProfileID, n.Database, n.Object, connections.MySQLTableRename)
				} else if s.selectedGroup != "" {
					s.beginGroupEdit(s.selectedGroup)
				}
			}
			if tree.Shortcut(0, ui.KeyDelete) {
				n := parseNavigationNode(s.explorerName)
				if n.Kind == "object" && n.Category == "表" {
					s.beginMySQLTableChange(n.ProfileID, n.Database, n.Object, connections.MySQLTableDrop)
				} else if n.Kind == "connection" && s.selectedProfileID != 0 {
					s.confirmDeleteProfile()
				}
			}
			if tree.Shortcut(ui.Ctrl, ui.KeyN) {
				n := parseNavigationNode(s.explorerName)
				if n.Kind == "database" || (n.Kind == "category" || n.Kind == "object") && n.Category == "表" {
					s.beginMySQLTableChange(n.ProfileID, n.Database, "", connections.MySQLTableCreate)
				}
			}
			if tree.Shortcut(ui.Ctrl, ui.KeyD) {
				n := parseNavigationNode(s.explorerName)
				if n.Kind == "object" && !isMySQLProgram(n.Category) {
					s.openMySQLStructure(c.Services(), n.Database, n.Object)
				}
			}
			if tree.Shortcut(0, ui.KeyF5) {
				n := parseNavigationNode(s.explorerName)
				if n.Database != "" {
					s.loadMySQLObjects(c.Services(), n.Database, true)
				} else if s.mysqlSessionStateFor(n.ProfileID) == "已连接" {
					s.refreshMySQLDatabases(c.Services())
				} else {
					s.loadProfiles()
				}
			}
			s.syncNavigationExpansion(c.Services())
		})
		s.sidebarFooter(c)
	})
}

// selectProfileByName ensures context commands target the right-clicked row,
// even when it is not the previously selected connection.
func (s *State) selectProfileByName(name string) {
	for _, p := range s.profileEntries {
		if p.Name != name {
			continue
		}
		// Keep background sessions alive, but show the selected connection's
		// own database browser instead of another profile's open data tab.
		if p.ID != s.selectedProfileID {
			if s.mysqlSessionProfileID == 0 && s.parkedMySQLWorkspace(p.ID) == nil {
				s.restoreMySQLWorkspace(0, nil)
			}
			t := s.currentTab()
			if (t.kind == tabStructure && t.structureProfileID != p.ID) ||
				(t.kind == tabMySQLData && t.dataProfileID != p.ID) ||
				(t.kind == tabProgram && t.programProfileID != p.ID) ||
				(t.kind == tabSQL && t.sqlProfileID != p.ID) {
				s.activeTab = 0
			}
		}
		s.activateMySQLProfile(p.ID)
		if s.selectedProfileID != p.ID || s.selectedGroup != "" {
			s.selectedProfileID = p.ID
			s.selectedGroup = ""
			s.explorerName = profileNode(p.ID)
			s.status = "已选择连接：" + name + "（" + s.mysqlSessionStateFor(p.ID) + "）"
		}
		s.explorerName = profileNode(p.ID)
		for i := 0; i < s.explorerTree.Rows(); i++ {
			if s.explorerTree.Item(i) == profileNode(p.ID) {
				s.explorerRow = i
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
		s.mysqlDataObjectInspector(c, s.currentTab())
		return
	}
	p, ok := s.currentProfile()
	ui.Column(c).Fill().Background(palette.Panel).Children(func() {
		s.inspectorHeader(c, "信息")
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(28, 14).Gap(16).Children(func() {
				if s.selectedGroup != "" {
					ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
						theme.Icon(c, "folder-filled", 50, palette.Gold)
						ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
							ui.Text(c, s.selectedGroup).FontSize(theme.Font(c, 14)).Bold()
							ui.Text(c, fmt.Sprintf("%s (%d %s)", locales.Text("虚拟组"), s.groupCount(s.selectedGroup), locales.Text("对象"))).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
						})
					})
					return
				}
				t := s.currentTab()
				if t.kind == tabStructure && t.structure != nil {
					ui.Text(c, t.structureTable).FontSize(theme.Font(c, 14)).Bold()
					mysqlInspectorProperty(c, locales.Text("数据库"), t.structureDatabase)
					if t.structureFieldRow >= 0 && t.structureFieldRow < len(t.structure.Columns) {
						field := t.structure.Columns[t.structureFieldRow]
						mysqlInspectorProperty(c, locales.Text("名称"), field.Name)
						mysqlInspectorProperty(c, locales.Text("类型"), field.Type)
						mysqlInspectorFieldAttributes(c, field)
					}
					return
				}
				if !ok {
					if s.selectedProfileID == 0 && s.selectedGroup == "" {
						ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
							theme.Icon(c, "connection", 48, palette.Muted)
							ui.Text(c, locales.Text("我的连接")).FontSize(theme.Font(c, 14)).Bold()
						})
						return
					}
					ui.Text(c, locales.Text("选择连接以查看档案信息")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					return
				}
				n := parseNavigationNode(s.explorerName)
				if n.Database != "" && s.mysqlSessionStateFor(p.ID) == "已连接" {
					name, icon := n.Database, "database"
					if n.Kind == "object" {
						name, icon = n.Object, "table"
					}
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						theme.Icon(c, icon, 32, palette.Blue)
						ui.Text(c, name).FontSize(theme.Font(c, 14)).Bold()
					})
					mysqlInspectorProperty(c, locales.Text("类型"), s.navigationDescription(n))
					mysqlInspectorProperty(c, locales.Text("连接"), p.Name)
					mysqlInspectorProperty(c, locales.Text("数据库"), n.Database)
					for _, obj := range s.mysqlObjectCache[n.Database] {
						if obj.Name == n.Object && obj.Kind == n.Category {
							mysqlInspectorProperty(c, locales.Text("引擎"), obj.Engine)
							mysqlInspectorProperty(c, locales.Text("注释"), obj.Comment)
							if isMySQLProgram(obj.Kind) {
								for _, field := range []struct{ label, value string }{
									{"定义者", obj.Definer}, {"参数", obj.Parameters},
									{"返回类型", obj.ReturnType}, {"SQL SECURITY", obj.SQLSecurity},
									{"状态", obj.Status}, {"调度", obj.Schedule},
									{"开始时间", obj.Starts}, {"结束时间", obj.Ends},
									{"ON COMPLETION", obj.OnCompletion}, {"创建时间", obj.CreatedAt},
									{"修改时间", obj.ModifiedAt},
								} {
									if field.value != "" {
										mysqlInspectorProperty(c, field.label, field.value)
									}
								}
							}
							break
						}
					}
					return
				}
				ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
					s.profileBrandIcon(c, p.Kind, 50)
					ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
						ui.Text(c, p.Name).FontSize(theme.Font(c, 14)).Bold()
						ui.Text(c, connectionKindName(p.Kind)).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					})
				})
				if p.Kind == connections.MySQL {
					s.mysqlSessionToolbar(c)
				}
				mysqlInspectorProperty(c, locales.Text("状态"), locales.Text(s.mysqlSessionStateFor(p.ID)))
				if p.Kind == connections.SQLite {
					mysqlInspectorProperty(c, locales.Text("文件"), p.Database)
					return
				}
				if p.Kind == connections.MySQL {
					resolved, err := connections.ResolveActiveSettings(p)
					if err != nil {
						mysqlInspectorProperty(c, locales.Text("活动配置"), locales.Text("无效，请编辑连接"))
						return
					}
					activeName := resolved.ConfigurationName
					if activeName == "" {
						activeName = locales.Text("主要配置文件")
					}
					mysqlInspectorProperty(c, locales.Text("活动配置"), activeName)
					mysqlInspectorProperty(c, locales.Text("主机"), resolved.Host)
					mysqlInspectorProperty(c, locales.Text("端口"), fmt.Sprint(resolved.Port))
					mysqlInspectorProperty(c, locales.Text("用户"), resolved.Username)
					mysqlInspectorProperty(c, locales.Text("数据库"), resolved.Database)
				} else {
					mysqlInspectorProperty(c, locales.Text("主机"), p.Host)
					mysqlInspectorProperty(c, locales.Text("端口"), fmt.Sprint(p.Port))
					mysqlInspectorProperty(c, locales.Text("用户"), p.Username)
					mysqlInspectorProperty(c, locales.Text("数据库"), p.Database)
				}
			})
		})
	})
}
