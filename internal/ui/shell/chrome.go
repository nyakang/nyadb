package shell

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) View(c *ui.Context) {
	s.consumeMySQLSessionResult()
	s.consumeMySQLObjects()
	s.consumeMySQLStructures()
	s.consumeMySQLData()
	s.consumeMySQLChunks()
	theme.Apply(c)
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		s.menuBar(c)
		s.toolBar(c)
		workbenchSplit(c, &s.sidebarPercent,
			func() {
				ui.Column(c).Fill().BorderWidth(0, theme.SplitterBorder, 0, 0).BorderColor(theme.Toolbar).Children(func() { s.sidebar(c) })
			},
			func() {
				if s.inspectorOn {
					workbenchSplit(c, &s.centerPercent,
						func() {
							ui.Column(c).Fill().BorderWidth(0, theme.SplitterBorder, 0, 0).BorderColor(theme.Toolbar).Children(func() { s.workspace(c) })
						},
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
	if s.profileMode && s.groupDeleteConfirm {
		if ui.AlertDialog(c, &s.groupDeleteConfirm, locales.Text("删除连接组？"), locales.Text("仅删除虚拟文件夹；组内连接将移至根目录，不会删除连接档案或数据库。"), locales.Text("取消"), locales.Text("删除组")) == 1 {
			s.deleteGroup()
		}
	}
	if s.profileMode && s.deleteConfirm {
		name := "所选连接"
		for _, p := range s.profileEntries {
			if p.ID == s.pendingDeleteID {
				name = p.Name
				break
			}
		}
		if ui.AlertDialog(c, &s.deleteConfirm, locales.Text("删除连接档案？"), "仅删除“"+name+"”的连接配置，不会删除数据库文件。", locales.Text("取消"), locales.Text("删除")) == 1 {
			s.deleteProfile()
		}
	}
}

// Keep the native drag/focus behavior while matching the pane separator fill.
func workbenchSplit(c *ui.Context, percent *float32, first, second func()) ui.Element {
	base := c.Theme()
	split := *base
	split.Border = theme.Toolbar
	c.SetTheme(&split)
	pane := func(draw func()) func() {
		return func() { c.SetTheme(base); draw(); c.SetTheme(&split) }
	}
	e := ui.SplitPercent(c, percent, pane(first), pane(second))
	c.SetTheme(base)
	return e
}

func (s *State) menuBar(c *ui.Context) {
	ui.Row(c).Height(theme.MenuHeight).AlignItems(ui.Center).Padding(0, 4).Gap(0).
		Background(theme.Menu).Children(func() {
		for _, label := range []string{"文件", "编辑", "查看", "收藏夹", "工具", "窗口", "帮助"} {
			name := label
			ui.ButtonBase(c).Label(locales.Text(name)).Padding(4, 8).Radius(theme.ControlRadius).
				Children(func() { ui.Text(c, locales.Text(name)).FontSize(theme.FontSize) }).
				Menu(func(m *ui.Menu) { s.menuCommands(name, m) })
		}
		ui.Box(c).Grow(1)
		ui.Text(c, "NyaDB").TextColor(theme.Muted).FontSize(theme.SmallFontSize).Padding(0, 6)
	})
}

// MyGo owns popup placement, keyboard navigation and dismissal.
func (s *State) menuCommands(section string, m *ui.Menu) {
	switch section {
	case "文件":
		if m.Item(locales.Text("新建连接…")).Chosen() {
			s.openConnectionDialog()
		}
		if m.Item(locales.Text("新建查询")).Chosen() {
			s.openQuery()
		}
		m.Separator()
		if m.Item(locales.Text("关闭当前页签")).Disabled(s.activeTab == 0).Chosen() {
			s.closeActive()
		}
	case "编辑":
		if m.Item(locales.Text("编辑连接")).Disabled(!s.profileMode || s.selectedProfileID == 0).Chosen() {
			s.startEditProfile()
		}
		if m.Item(locales.Text("复制连接")).Disabled(!s.profileMode || s.selectedProfileID == 0).Chosen() {
			s.duplicateProfile()
		}
	case "查看":
		if m.Item(locales.Text("信息面板")).Checked(s.inspectorOn).Chosen() {
			s.inspectorOn = !s.inspectorOn
			s.status = "已切换右侧信息面板"
		}
		m.Separator()
		m.Submenu(locales.Text("数据库对象"), func(sub *ui.Menu) {
			for _, kind := range []string{"表", "视图", "函数"} {
				if sub.Item(locales.Text(kind)).Checked(s.objectKind == kind).Disabled(!s.toolbarActionAvailable(kind)).Chosen() {
					s.showObjects(kind)
				}
			}
		})
		m.Separator()
		m.Submenu(locales.Text("语言"), func(sub *ui.Menu) {
			if sub.Item(locales.Text("跟随系统语言")).Chosen() {
				mygo.App.SetLocale("")
			}
			sub.Separator()
			for _, language := range locales.Catalog.Languages() {
				tag := language
				if sub.Item(i18n.Autonym(tag)).Checked(i18n.Locale() == tag).Chosen() {
					mygo.App.SetLocale(tag)
				}
			}
		})
		m.Submenu(locales.Text("主题"), func(sub *ui.Menu) {
			for _, choice := range []struct {
				mode theme.Mode
				name string
			}{
				{theme.ModeSystem, "跟随系统"}, {theme.ModeLight, "浅色"}, {theme.ModeDark, "深色"},
			} {
				if sub.Item(locales.Text(choice.name)).Checked(theme.CurrentMode() == choice.mode).Chosen() {
					s.changeAppearance(choice.mode)
				}
			}
			sub.Separator()
			if sub.Item(locales.Text("主题设置…")).Chosen() {
				s.openSettings()
			}
		})
	case "工具":
		// Preserve the observed compact Navicat menu hierarchy without
		// pretending that the unimplemented database tools can be executed.
		for _, label := range []string{"数据传输…", "数据生成…", "数据脱敏…", "数据字典…", "数据同步…", "结构同步…"} {
			m.Item(locales.Text(label)).Disabled(true)
		}
		m.Item(locales.Text("命令列界面…")).Shortcut(0, ui.KeyF6).Disabled(true)
		m.Submenu(locales.Text("服务器监控"), func(sub *ui.Menu) {
			sub.Item(locales.Text("管理服务器监控")).Disabled(true)
		}).Disabled(true)
		m.Item(locales.Text("在数据库或模式中查找…")).Shortcut(ui.Ctrl|ui.Shift, ui.KeyF).Disabled(true)
		m.Separator()
		m.Item(locales.Text("历史日志…")).Shortcut(ui.Ctrl, ui.KeyL).Disabled(true)
		m.Separator()
		if m.Item(locales.Text("选项…")).Chosen() {
			s.openSettings()
		}
	case "窗口":
		if m.Item(locales.Text("新建 SQL 页签")).Chosen() {
			s.openQuery()
		}
		if m.Item(locales.Text("关闭当前页签")).Disabled(s.activeTab == 0).Chosen() {
			s.closeActive()
		}
	case "帮助":
		if m.Item(locales.Text("关于 NyaDB")).Chosen() {
			s.status = "NyaDB · Go + MyGo Native UI"
		}
	default:
		m.Item(locales.Text("功能开发中")).Disabled(true)
	}
}

// SetAppearanceHandler is installed by bootstrap to persist the preference,
// change the OS-native theme and refresh all NyaDB windows.
func (s *State) SetAppearanceHandler(fn func(theme.Mode)) { s.appearanceChanged = fn }

func (s *State) changeAppearance(mode theme.Mode) {
	theme.SetMode(mode)
	if s.appearanceChanged != nil {
		s.appearanceChanged(mode)
	}
}

type toolAction struct {
	label, icon string
	color       ui.Color
}

func (s *State) toolBar(c *ui.Context) {
	actions := []toolAction{
		{"连接", "connection", theme.Blue},
		{"新建查询", "new-query", theme.Blue},
		{"表", "table", theme.Blue},
		{"视图", "view", theme.Blue},
		{"函数", "function", theme.Blue},
		{"用户", "user", theme.Gold},
		{"其它", "tools", theme.Blue},
		{"查询", "query", theme.Blue},
		{"备份", "backup", theme.Blue},
		{"自动运行", "automation", theme.Green},
		{"模型", "model", theme.Gold},
	}
	ui.Row(c).Height(theme.ToolbarHeight).AlignItems(ui.Center).Padding(1, 4).Gap(1).
		Background(theme.Toolbar).Children(func() {
		ui.Toolbar(c, func() {
			for _, action := range actions {
				item := action
				active := s.objectKind == item.label && s.currentTab().kind == tabObjects
				button := ui.Button(c, "").Label(locales.Text(item.label)).Tooltip(locales.Text(item.label)).
					Width(theme.ToolbarItemWidth).Height(theme.ToolbarItemHeight).Padding(3, 2).Radius(theme.ControlRadius).
					Disabled(!s.toolbarActionAvailable(item.label)).Border(0, ui.Transparent).
					Children(func() {
						ui.Column(c).Center().Gap(4).Children(func() {
							theme.Icon(c, item.icon, theme.ToolbarIconSize, item.color)
							ui.Text(c, locales.Text(item.label)).FontSize(theme.FontSize).SingleLine()
						})
					})
				styleToolbarButton(button, active)
				if button.Clicked() {
					s.clickToolbar(item.label)
				}
			}
		}).Label(locales.Text("主工具栏")).Grow(1).MinWidth(0).Padding(0).Gap(1).Background(theme.Toolbar)
		// Keep the inspector toggle available even when toolbar actions overflow.
		info := ui.Button(c, "").Label(locales.Text("信息")).Tooltip(locales.Text("显示或隐藏信息面板")).
			Width(48).Height(theme.ToolbarItemHeight).Padding(3, 2).Radius(theme.ControlRadius).Border(0, ui.Transparent).Children(func() {
			ui.Column(c).Center().Gap(4).Children(func() {
				theme.Icon(c, "info", theme.ToolbarIconSize, theme.Blue)
				ui.Text(c, locales.Text("信息")).FontSize(theme.FontSize)
			})
		})
		styleToolbarButton(info, false)
		if info.Clicked() {
			s.clickToolbar("信息")
		}
	})
}

// Disabled commands keep their place in the native toolbar and its overflow
// menu, but never announce a successful operation for an unsupported feature.
func (s *State) toolbarActionAvailable(action string) bool {
	switch action {
	case "连接", "新建查询", "表", "视图", "信息":
		return true
	case "函数":
		return !s.profileMode
	default:
		return false
	}
}

func styleToolbarButton(button ui.Element, selected bool) {
	background := theme.Toolbar
	if selected {
		background = theme.ToolbarSelected
		button.Border(theme.BorderWidth, theme.Border)
	}
	if button.Hovered() {
		background = theme.Hover
	}
	if button.Pressed() {
		background = theme.Pressed
	}
	button.Background(background)
}

func (s *State) mysqlWorkbenchStatus() string {
	if s.mysqlSessionStatus == "" {
		return "未连接"
	}
	return s.mysqlSessionStatus
}

func (s *State) statusBar(c *ui.Context) {
	ui.Row(c).Height(theme.StatusBarHeight).Padding(1, 6).AlignItems(ui.Center).Gap(10).
		Background(theme.Menu).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
		ui.Text(c, locales.Text(s.status)).FontSize(theme.SmallFontSize).TextColor(theme.Text).SingleLine().Grow(1).MinWidth(0)
		if s.profileMode {
			ui.Text(c, fmt.Sprintf("%d %s", len(s.profileEntries), locales.Text("个连接档案"))).FontSize(11).TextColor(theme.Muted)
			ui.Text(c, "●  "+locales.Text(s.mysqlWorkbenchStatus())).FontSize(11).TextColor(theme.Blue)
			return
		}
		ui.Text(c, "5 "+locales.Text("个模拟连接")).FontSize(11).TextColor(theme.Muted)
		ui.Text(c, "●  UI Preview").FontSize(11).TextColor(theme.Blue)
	})
}
