package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) View(c *ui.Context) {
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
}

func (s *State) menuBar(c *ui.Context) {
	ui.Row(c).Height(31).AlignItems(ui.Center).Padding(0, 10).Gap(2).
		Background(theme.Menu).Children(func() {
		for _, label := range []string{"文件", "编辑", "查看", "收藏夹", "工具", "窗口", "帮助"} {
			name := label
			if ui.Button(c, name).Padding(4, 10).Clicked() {
				if name == "查看" {
					s.inspectorOn = !s.inspectorOn
					s.status = "已切换右侧信息面板"
				} else {
					s.status = name + "菜单：更多命令将在后续阶段开放"
				}
			}
		}
		ui.Box(c).Grow(1)
		ui.Text(c, "NyaDB").TextColor(theme.Muted).FontSize(11)
	})
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
	ui.ScrollHorizontal(c).Height(84).Background(theme.Toolbar).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).Padding(2, 10).Gap(3).Children(func() {
			for _, action := range actions {
				item := action
				active := s.objectKind == item.label && s.currentTab().kind == tabObjects
				button := ui.Button(c, "").Label(item.label).Tooltip(item.label).
					Width(78).Height(77).Padding(5, 2).
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
			ui.Box(c).Grow(1)
			if ui.Button(c, "").Label("信息").Tooltip("显示或隐藏信息面板").
				Width(65).Height(77).Padding(5, 3).Children(func() {
				ui.Column(c).Center().Gap(5).Children(func() {
					theme.Icon(c, "info", 26, theme.Blue)
					ui.Text(c, "信息").FontSize(12)
				})
			}).Clicked() {
				s.clickToolbar("信息")
			}
		})
	})
}

func (s *State) statusBar(c *ui.Context) {
	ui.Row(c).Height(25).Padding(2, 10).AlignItems(ui.Center).Gap(12).
		Background(theme.Menu).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
		ui.Text(c, s.status).FontSize(11).TextColor(theme.Text).SingleLine().Grow(1)
		ui.Text(c, "5 个模拟连接").FontSize(11).TextColor(theme.Muted)
		ui.Text(c, "●  UI Preview").FontSize(11).TextColor(theme.Blue)
	})
}
