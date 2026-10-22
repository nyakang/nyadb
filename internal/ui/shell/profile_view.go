package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) profileSidebar(c *ui.Context) {
	ui.Column(c).Fill().Background(theme.Sidebar).Children(func() {
		ui.Row(c).Height(27).Padding(2, 8).AlignItems(ui.Center).Background(theme.Sidebar).Gap(6).Children(func() {
			theme.Icon(c, "connection", 15, theme.Muted)
			ui.Text(c, "我的连接").FontSize(12).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, "").Label("新建组").Tooltip("新建虚拟连接组").Padding(1, 5).
				Children(func() { theme.Icon(c, "new-group", 15, theme.Gold) }).Clicked() {
				s.beginGroupEdit("")
			}
			if ui.Button(c, "").Label("添加连接").Tooltip("新建连接").Padding(1, 5).
				Children(func() { theme.Icon(c, "plus", 15, theme.Blue) }).Clicked() {
				s.openConnectionDialog()
			}
		})
		if s.groupEditing {
			ui.Row(c).Padding(3, 6).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &s.groupDraft).Label("连接组名称").Placeholder("组名称").Grow(1)
				if ui.Button(c, "保存").Label("保存组").Clicked() {
					s.saveGroup()
				}
				if ui.Button(c, "×").Label("取消编辑组").Clicked() {
					s.groupEditing = false
				}
			})
			if s.groupError != "" {
				ui.Text(c, s.groupError).Padding(2, 8).TextColor(theme.Gold).FontSize(11)
			}
		}
		ui.Column(c).Grow(1).Padding(2, 3).Gap(0).Children(func() {
			roots := s.profileRoots()
			if len(roots) == 0 {
				msg := "暂无连接，使用右上角 + 新建"
				if strings.TrimSpace(s.search) != "" {
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
							if m.Item("新建连接").Chosen() {
								s.selectGroup(name)
								s.openConnectionDialog()
							}
							if m.Item("重命名组").Chosen() {
								s.beginGroupEdit(name)
							}
							if m.Item("删除连接组").Chosen() {
								s.groupPendingDelete = name
								s.groupDeleteConfirm = true
							}
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
						if m.Item("编辑连接").Chosen() {
							s.selectProfileByName(name)
							s.startEditProfile()
						}
						if m.Item("复制连接").Chosen() {
							s.selectProfileByName(name)
							s.duplicateProfile()
						}
						if m.Item("删除连接").Chosen() {
							s.selectProfileByName(name)
							s.confirmDeleteProfile()
						}
						m.Separator()
						m.Submenu("移动到组", func(sub *ui.Menu) {
							id := int64(0)
							for _, p := range s.profileEntries {
								if p.Name == name {
									id = p.ID
									break
								}
							}
							if sub.Item("未分组").Chosen() {
								s.moveProfileToGroup(id, "")
							}
							for _, g := range s.profileGroups {
								group := g.Name
								if sub.Item(group).Chosen() {
									s.moveProfileToGroup(id, group)
								}
							}
						})
						if m.Item("刷新").Chosen() {
							s.loadProfiles()
						}
					})
				}).Grow(1)
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
		ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Padding(3, 5).Gap(2).Children(func() {
			s.mysqlSessionToolbar(c)
			ui.Row(c).Height(30).Gap(3).AlignItems(ui.Center).Children(func() {
				if ui.SearchField(c, &s.search).Label("搜索连接").Placeholder("搜索连接").Grow(1).MinWidth(0).Changed() {
					s.explorerRow = -1
				}
				has := s.selectedProfileID > 0
				if ui.Button(c, "").Label("编辑连接").Tooltip("编辑选中连接").Disabled(!has).Padding(1, 4).
					Children(func() { theme.Icon(c, "edit", 14, theme.Muted) }).Clicked() {
					s.startEditProfile()
				}
				if ui.Button(c, "").Label("复制连接").Tooltip("复制选中连接").Disabled(!has).Padding(1, 4).
					Children(func() { theme.Icon(c, "copy", 14, theme.Muted) }).Clicked() {
					s.duplicateProfile()
				}
				if ui.Button(c, "").Label("删除连接").Tooltip("删除选中连接").Disabled(!has).Padding(1, 4).
					Children(func() { theme.Icon(c, "delete", 14, theme.Muted) }).Clicked() {
					s.confirmDeleteProfile()
				}
				if ui.Button(c, "").Label("刷新").Tooltip("刷新连接列表").Padding(1, 4).
					Children(func() { theme.Icon(c, "refresh", 14, theme.Blue) }).Clicked() {
					s.loadProfiles()
				}
			})
		})
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
			ui.Text(c, "信息").Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label("隐藏信息面板").Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Column(c).Padding(14).Gap(10).Children(func() {
			if s.selectedGroup != "" {
				ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
					theme.Icon(c, "folder-filled", 45, theme.Gold)
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, s.selectedGroup).FontSize(15).Bold()
						ui.Text(c, fmt.Sprintf("虚拟组 (%d 对象)", s.groupCount(s.selectedGroup))).FontSize(11).TextColor(theme.Muted)
					})
				})
				return
			}
			if !ok {
				ui.Text(c, "选择连接以查看档案信息").TextColor(theme.Muted)
				return
			}
			s.profileBrandIcon(c, p.Kind, 38)
			ui.Text(c, p.Name).Bold().FontSize(16)
			ui.Text(c, "配置档案 / "+s.mysqlSessionStateFor(p.ID)).FontSize(11).TextColor(theme.Muted)
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Collapsible(c, "基本信息", &s.inspectorDetailsOpen, func() {
				s.property(c, "名称", p.Name)
				s.property(c, "类型", connectionKindName(p.Kind))
				s.property(c, "状态", s.mysqlSessionStateFor(p.ID))
				if p.Kind == connections.SQLite {
					s.property(c, "文件", p.Database)
				} else if p.Kind == connections.MySQL {
					resolved, err := connections.ResolveActiveSettings(p)
					if err != nil {
						s.property(c, "活动配置", "无效，请编辑连接")
					} else {
						activeName := "主要配置文件"
						if resolved.ConfigurationName != "" {
							activeName = resolved.ConfigurationName
						}
						s.property(c, "活动配置", activeName)
						s.property(c, "主机", resolved.Host)
						s.property(c, "端口", fmt.Sprint(resolved.Port))
						s.property(c, "用户", resolved.Username)
						s.property(c, "数据库", resolved.Database)
					}
				} else {
					s.property(c, "主机", p.Host)
					s.property(c, "端口", fmt.Sprint(p.Port))
					s.property(c, "用户", p.Username)
					s.property(c, "数据库", p.Database)
				}
			})
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Text(c, "连接状态通过工作台显式操作管理；密码不会保存。").TextColor(theme.Muted).FontSize(11)
		})
	})
}
