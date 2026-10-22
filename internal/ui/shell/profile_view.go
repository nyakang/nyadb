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
	ui.Column(c).Fill().Background(theme.Sidebar).Children(func() {
		header := ui.Row(c).Height(27).Padding(2, 8).AlignItems(ui.Center).Background(theme.Sidebar).Gap(6)
		header.Children(func() {
			theme.Icon(c, "connection", 15, theme.Muted)
			ui.Text(c, locales.Text("我的连接")).FontSize(12).Bold()
			ui.Box(c).Grow(1)
			if header.Hovered() {
				settings := ui.ButtonBase(c).Label(locales.Text("连接面板设置")).Tooltip(locales.Text("连接面板设置")).Padding(3, 4).Radius(2)
				settings.Children(func() { theme.Icon(c, "settings", 15, theme.Muted) })
				settings.Menu(func(m *ui.Menu) { s.sidebarSettingsMenu(m) })
			}
		})
		header.ContextMenu(func(m *ui.Menu) { s.sidebarSettingsMenu(m) })
		if s.groupEditing {
			ui.Row(c).Padding(3, 6).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &s.groupDraft).Label(locales.Text("连接组名称")).Placeholder(locales.Text("组名称")).Grow(1)
				if ui.Button(c, locales.Text("保存")).Label(locales.Text("保存组")).Clicked() {
					s.saveGroup()
				}
				if ui.Button(c, "×").Label(locales.Text("取消编辑组")).Clicked() {
					s.groupEditing = false
				}
			})
			if s.groupError != "" {
				ui.Text(c, s.groupError).Padding(2, 8).TextColor(theme.Gold).FontSize(11)
			}
		}
		navigator := ui.Column(c).Grow(1).Padding(2, 3).Gap(0)
		navigator.Children(func() {
			roots := s.profileRoots()
			if len(roots) == 0 {
				msg := "暂无连接，右键空白处新建连接"
				if strings.TrimSpace(s.search) != "" || s.profileStarredOnly || s.profileEngine != "" {
					msg = "没有匹配的连接或组"
				}
				ui.Text(c, msg).Padding(8).TextColor(theme.Muted).FontSize(11)
			} else {
				if s.explorerRow < 0 {
					s.explorerRow = s.outlineRowForSelection(roots)
				}
				s.explorerTree.List.Selected = &s.explorerRow
				ui.Outline(c, &s.explorerTree, roots, s.profileChildren, func(node string) {
					if isGroupNode(node) {
						name := groupFromNode(node)
						ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
							theme.Icon(c, "folder-filled", 15, theme.Gold)
							ui.Text(c, name).SingleLine().FontSize(12)
						}).ContextMenu(func(m *ui.Menu) {
							s.profileGroupMenu(m, name)
						})
						return
					}
					name := node
					ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
						for _, p := range s.profileEntries {
							if p.Name == name {
								s.profileBrandIcon(c, p.Kind, 16)
								break
							}
						}
						ui.Text(c, name).SingleLine().FontSize(12)
					}).ContextMenu(func(m *ui.Menu) {
						s.profileConnectionMenu(m, name, c.Services())
					})
				}).Grow(1).ContextMenu(func(m *ui.Menu) {
					s.profileBlankMenu(m)
				})
				if s.explorerRow < 0 {
					for i := 0; i < s.explorerTree.Rows(); i++ {
						if s.explorerTree.Item(i) == s.explorerName {
							s.explorerRow = i
							break
						}
					}
				}
				if s.explorerRow >= 0 && s.explorerRow < s.explorerTree.Rows() {
					node := s.explorerTree.Item(s.explorerRow)
					if node != s.explorerName {
						if isGroupNode(node) {
							s.selectGroup(groupFromNode(node))
						} else {
							s.selectProfileByName(node)
						}
					}
				}
			}
			ui.Scroll(c).Children(func() { s.mysqlDatabaseTree(c) })
		})
		navigator.ContextMenu(func(m *ui.Menu) { s.profileBlankMenu(m) })
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
		if s.selectedProfileID != p.ID || s.selectedGroup != "" {
			s.selectedProfileID = p.ID
			s.selectedGroup = ""
			s.explorerName = p.Name
			s.status = "已选择连接：" + name + "（" + s.mysqlSessionStateFor(p.ID) + "）"
		}
		for i := 0; i < s.explorerTree.Rows(); i++ {
			if s.explorerTree.Item(i) == name {
				s.explorerRow = i
				break
			}
		}
		return
	}
}

func (s *State) profileInspector(c *ui.Context) {
	if s.currentTab().kind == tabMySQLData {
		s.mysqlDataInspector(c, s.currentTab())
		return
	}
	p, ok := s.currentProfile()
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		ui.Row(c).Height(25).Padding(2, 9).AlignItems(ui.Center).Background(theme.Panel).Children(func() {
			ui.Text(c, locales.Text("信息")).Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label(locales.Text("隐藏信息面板")).Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Column(c).Padding(14).Gap(10).Children(func() {
			if s.selectedGroup != "" {
				ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
					theme.Icon(c, "folder-filled", 45, theme.Gold)
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, s.selectedGroup).FontSize(15).Bold()
						ui.Text(c, fmt.Sprintf("%s (%d %s)", locales.Text("虚拟组"), s.groupCount(s.selectedGroup), locales.Text("对象"))).FontSize(11).TextColor(theme.Muted)
					})
				})
				return
			}
			if !ok {
				ui.Text(c, locales.Text("选择连接以查看档案信息")).TextColor(theme.Muted)
				return
			}
			s.profileBrandIcon(c, p.Kind, 38)
			ui.Text(c, p.Name).Bold().FontSize(16)
			ui.Text(c, locales.Text("配置档案 / ")+locales.Text(s.mysqlSessionStateFor(p.ID))).FontSize(11).TextColor(theme.Muted)
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Collapsible(c, locales.Text("基本信息"), &s.inspectorDetailsOpen, func() {
				s.property(c, locales.Text("名称"), p.Name)
				s.property(c, locales.Text("类型"), connectionKindName(p.Kind))
				s.property(c, locales.Text("状态"), locales.Text(s.mysqlSessionStateFor(p.ID)))
				if p.Kind == connections.SQLite {
					s.property(c, locales.Text("文件"), p.Database)
				} else if p.Kind == connections.MySQL {
					resolved, err := connections.ResolveActiveSettings(p)
					if err != nil {
						s.property(c, locales.Text("活动配置"), locales.Text("无效，请编辑连接"))
					} else {
						activeName := "主要配置文件"
						if resolved.ConfigurationName != "" {
							activeName = resolved.ConfigurationName
						}
						s.property(c, locales.Text("活动配置"), activeName)
						s.property(c, locales.Text("主机"), resolved.Host)
						s.property(c, locales.Text("端口"), fmt.Sprint(resolved.Port))
						s.property(c, locales.Text("用户"), resolved.Username)
						s.property(c, locales.Text("数据库"), resolved.Database)
					}
				} else {
					s.property(c, locales.Text("主机"), p.Host)
					s.property(c, locales.Text("端口"), fmt.Sprint(p.Port))
					s.property(c, locales.Text("用户"), p.Username)
					s.property(c, locales.Text("数据库"), p.Database)
				}
			})
			ui.Box(c).Height(1).Background(theme.Border)
			if p.Kind == connections.MySQL {
				s.mysqlSessionToolbar(c)
			}
			ui.Text(c, locales.Text("连接状态通过工作台显式操作管理；密码不会保存。")).TextColor(theme.Muted).FontSize(11)
		})
	})
}
