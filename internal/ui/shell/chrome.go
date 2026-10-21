package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) View(c *ui.Context) {
	s.consumeMySQLSessionResult()
	s.consumeMySQLObjects()
	s.consumeMySQLStructures()
	theme.Apply(c)
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		s.menuBar(c)
		s.toolBar(c)
		ui.SplitPercent(c, &s.sidebarPercent,
			func() { s.sidebar(c) },
			func() {
				if s.inspectorOn {
					ui.SplitPercent(c, &s.centerPercent,
						func() { s.workspace(c) },
						func() { s.inspector(c) },
					).Fill()
				} else {
					s.workspace(c)
				}
			},
		).Grow(1)
		s.statusBar(c)
	})
	s.connectionModal(c)
	if s.profileMode && s.deleteConfirm {
		name := "所选连接"
		for _, p := range s.profileEntries {
			if p.ID == s.pendingDeleteID {
				name = p.Name
				break
			}
		}
		if ui.AlertDialog(c, &s.deleteConfirm, "删除连接档案？", "仅删除“"+name+"”的连接配置，不会删除数据库文件。", "取消", "删除") == 1 {
			s.deleteProfile()
		}
	}
}

func (s *State) menuBar(c *ui.Context) {
	ui.Row(c).Height(31).AlignItems(ui.Center).Padding(0, 10).Gap(2).
		Background(theme.Menu).Children(func() {
		for _, label := range []string{"文件", "编辑", "查看", "收藏夹", "工具", "窗口", "帮助"} {
			name := label
			ui.ButtonBase(c).Label(name).Padding(4, 10).Radius(3).
				Children(func() { ui.Text(c, name).FontSize(12) }).
				Menu(func(m *ui.Menu) { s.menuCommands(name, m) })
		}
		ui.Box(c).Grow(1)
		ui.Text(c, "NyaDB").TextColor(theme.Muted).FontSize(11)
	})
}

// MyGo owns popup placement, keyboard navigation and dismissal.
func (s *State) menuCommands(section string, m *ui.Menu) {
	switch section {
	case "文件":
		if m.Item("新建连接…").Chosen() {
			s.openConnectionDialog()
		}
		if m.Item("新建查询").Chosen() {
			s.openQuery()
		}
		m.Separator()
		if m.Item("关闭当前页签").Disabled(s.activeTab == 0).Chosen() {
			s.closeActive()
		}
	case "编辑":
		if m.Item("编辑连接").Disabled(!s.profileMode || s.selectedProfileID == 0).Chosen() {
			s.startEditProfile()
		}
		if m.Item("复制连接").Disabled(!s.profileMode || s.selectedProfileID == 0).Chosen() {
			s.duplicateProfile()
		}
	case "查看":
		if m.Item("信息面板").Checked(s.inspectorOn).Chosen() {
			s.inspectorOn = !s.inspectorOn
			s.status = "已切换右侧信息面板"
		}
		m.Separator()
		m.Submenu("数据库对象", func(sub *ui.Menu) {
			for _, kind := range []string{"表", "视图", "函数"} {
				if sub.Item(kind).Checked(s.objectKind == kind).Chosen() {
					s.showObjects(kind)
				}
			}
		})
	case "窗口":
		if m.Item("新建 SQL 页签").Chosen() {
			s.openQuery()
		}
		if m.Item("关闭当前页签").Disabled(s.activeTab == 0).Chosen() {
			s.closeActive()
		}
	case "帮助":
		if m.Item("关于 NyaDB").Chosen() {
			s.status = "NyaDB · Go + MyGo Native UI"
		}
	default:
		m.Item("功能开发中").Disabled(true)
	}
}

type toolAction struct {
	label, icon string
	color       ui.Color
}

func (s *State) toolBar(c *ui.Context) {
	actions := []toolAction{
		{"连接", "connection", theme.Blue},
		{"新建查询", "query", theme.Blue},
		{"表", "table", theme.Blue},
		{"视图", "view", theme.Blue},
		{"函数", "function", theme.Blue},
		{"用户", "user", theme.Gold},
		{"其它", "tools", theme.Blue},
		{"查询", "query", theme.Blue},
		{"备份", "backup", theme.Muted},
		{"自动运行", "automation", theme.Green},
		{"模型", "model", theme.Gold},
	}
	ui.Row(c).Height(84).AlignItems(ui.Center).Padding(2, 10).Gap(3).
		Background(theme.Toolbar).Children(func() {
		ui.Toolbar(c, func() {
			for _, action := range actions {
				item := action
				active := s.objectKind == item.label && s.currentTab().kind == tabObjects
				button := ui.Button(c, "").Label(item.label).Tooltip(item.label).
					Width(78).Height(77).Padding(5, 2).Border(1, theme.Border).Radius(3).
					Children(func() {
						ui.Column(c).Center().Gap(5).Children(func() {
							theme.Icon(c, item.icon, 27, item.color)
							ui.Text(c, item.label).FontSize(12).SingleLine()
						})
					})
				if active {
					button.Background(theme.Accent.Alpha(0.36))
				} else {
					button.Background(theme.Toolbar)
				}
				if button.Clicked() {
					s.clickToolbar(item.label)
				}
			}
		}).Label("主工具栏").Grow(1).MinWidth(0).Padding(0).Gap(3).Background(theme.Toolbar)
		// Keep the inspector toggle available even when toolbar actions overflow.
		if ui.Button(c, "").Label("信息").Tooltip("显示或隐藏信息面板").
			Width(65).Height(77).Padding(5, 3).Border(1, theme.Border).Radius(3).Children(func() {
			ui.Column(c).Center().Gap(5).Children(func() {
				theme.Icon(c, "info", 26, theme.Blue)
				ui.Text(c, "信息").FontSize(12)
			})
		}).Clicked() {
			s.clickToolbar("信息")
		}
	})
}

func (s *State) mysqlWorkbenchStatus() string {
	if s.mysqlSessionStatus == "" {
		return "未连接"
	}
	return s.mysqlSessionStatus
}

func (s *State) statusBar(c *ui.Context) {
	ui.Row(c).Height(25).Padding(2, 10).AlignItems(ui.Center).Gap(12).
		Background(theme.Menu).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
		ui.Text(c, s.status).FontSize(11).TextColor(theme.Text).SingleLine().Grow(1)
		if s.profileMode {
			ui.Text(c, fmt.Sprintf("%d 个连接档案", len(s.profileEntries))).FontSize(11).TextColor(theme.Muted)
			ui.Text(c, "●  "+s.mysqlWorkbenchStatus()).FontSize(11).TextColor(theme.Blue)
			return
		}
		ui.Text(c, "5 个模拟连接").FontSize(11).TextColor(theme.Muted)
		ui.Text(c, "●  UI Preview").FontSize(11).TextColor(theme.Blue)
	})
}
