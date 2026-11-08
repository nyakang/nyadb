package shell

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) View(c *ui.Context) {
	s.editServices = c.Services()
	s.consumeWorkspaces()
	s.workbenchKeyboard(c)
	s.sqlWorkspace.Invalidate = c.Services().Invalidate
	if s.profileMode {
		s.activateMySQLForCurrentTab()
	}
	if s.profileMode {
		s.sqlWorkspace.ActivateAssets(s.selectedProfileID)
	}
	s.startAutoConnect(c.Services())
	s.sessions.Consume()
	_ = s.consumeInitialSQLCleanup()
	s.objectWorkspace.Consume(c.Services())
	s.dataWorkspace.Consume(c.Services())
	s.sqlWorkspace.ConsumeDocuments(c.Services())
	s.sqlWorkspace.Consume()
	s.favorites.Invalidate = c.Services().Invalidate
	s.favorites.Consume()
	s.observeSQLDocuments()
	theme.Apply(c)

	palette := theme.Colors(c)
	ui.Column(c).Fill().Background(palette.App).Children(func() {
		s.menuBar(c)
		s.toolBar(c)
		workbenchSplit(c, &s.sidebarPercent,
			func() {
				ui.Column(c).Fill().BorderWidth(0, theme.SplitterBorder, 0, 0).BorderColor(palette.Toolbar).Children(func() { s.sidebar(c) })
			},
			func() {
				if s.inspectorVisible() {
					workbenchSplit(c, &s.centerPercent,
						func() {
							ui.Column(c).Fill().BorderWidth(0, theme.SplitterBorder, 0, 0).BorderColor(palette.Toolbar).Children(func() { s.workspace(c) })
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
	s.workspaceManagerView(c)
	s.tunnelConfirmation(c)
	s.sqlWorkspace.Modals(c)
	s.connectionModal(c)
	s.objectWorkspace.Modals(c)
	s.dataWorkspace.Modals(c)
	s.favorites.View(c)
	if s.profileMode && s.navigation.GroupDeleteConfirm {
		if ui.AlertDialog(c, &s.navigation.GroupDeleteConfirm, locales.Text("删除连接组？"), locales.Text("仅删除虚拟文件夹；组内连接将移至根目录，不会删除连接档案或数据库。"), locales.Text("取消"), locales.Text("删除组")) == 1 {
			s.navigation.DeleteGroup()
		}
	}
	if s.profileMode && s.deleteConfirm {
		name := "所选连接"
		for _, p := range s.navigation.Profiles() {
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
	palette := theme.Colors(c)

	base := c.Theme()
	split := *base
	split.Border = palette.Toolbar
	c.SetTheme(&split)
	pane := func(draw func()) func() {
		return func() { c.SetTheme(base); draw(); c.SetTheme(&split) }
	}
	e := ui.SplitPercent(c, percent, pane(first), pane(second))
	c.SetTheme(base)
	return e
}

func (s *State) menuBar(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, theme.MenuHeight)).AlignItems(ui.Center).Padding(0, 4).Gap(0).
		Background(palette.Menu).Children(func() {
		for _, label := range []string{"文件", "编辑", "查看", "收藏夹", "工具", "窗口", "帮助"} {
			name := label
			if name == "编辑" && s.editMenuHost != nil {
				if ui.ButtonBase(c).Label(locales.Text(name)).Padding(4, 8).Radius(theme.ControlRadius).Children(func() { ui.Text(c, locales.Text(name)).FontSize(theme.Font(c, theme.FontSize)) }).Clicked() {
					s.openEditMenu()
				}
				continue
			}
			ui.ButtonBase(c).Label(locales.Text(name)).Padding(4, 8).Radius(theme.ControlRadius).
				Children(func() { ui.Text(c, locales.Text(name)).FontSize(theme.Font(c, theme.FontSize)) }).
				Menu(func(m *ui.Menu) { s.menuCommands(name, m) })
		}
		ui.Box(c).Grow(1)
		ui.Text(c, "NyaDB").TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(0, 6)
	})
}

// MyGo owns popup placement, keyboard navigation and dismissal.
func (s *State) menuCommands(section string, m *ui.Menu) {
	switch section {
	case "收藏夹":
		s.favoritesMenu(m)
	case "文件":
		s.documentMenuCommands(section, m)
	case "编辑":
		s.documentMenuCommands(section, m)
	case "查看":
		if m.Item(locales.Text("信息面板")).Checked(s.inspectorOn).Chosen() {
			s.inspectorOn = !s.inspectorOn
			s.status = "已切换右侧信息面板"
		}
		m.Separator()
		m.Submenu(locales.Text("数据库对象"), func(sub *ui.Menu) {
			for _, kind := range []string{"表", "视图", "函数"} {
				if sub.Item(locales.Text(kind)).Checked(s.currentObjectKind() == kind).Disabled(!s.toolbarActionAvailable(kind)).Chosen() {
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
			sub.Submenu(locales.Text("配色方案"), func(colors *ui.Menu) {
				for _, scheme := range theme.Schemes() {
					if colors.Item(locales.Text(scheme.Name)).Checked(theme.CurrentScheme() == scheme.ID).Chosen() {
						s.changeColorScheme(scheme.ID)
					}
				}
			})
			sub.Separator()
			if sub.Item(locales.Text("主题设置…")).Chosen() {
				s.openSettings()
			}
		})
	case "工具":
		s.documentMenuCommands(section, m)
		s.aiMenuCommands(m)
		m.Separator()
		// Preserve the observed compact Navicat menu hierarchy without
		// pretending that the unimplemented database tools can be executed.
		for _, label := range []string{"数据传输…", "数据生成…", "数据脱敏…"} {
			m.Item(locales.Text(label) + " · " + theme.AvailabilityText(contract.Availability{Reason: "not_implemented"})).Disabled(true)
		}
		s.objectWorkspace.DictionaryMenu(m)
		for _, label := range []string{"数据同步…", "结构同步…"} {
			m.Item(locales.Text(label)).Disabled(true)
		}
		m.Item(locales.Text("命令列界面…")).Shortcut(0, ui.KeyF6).Disabled(true)
		m.Submenu(locales.Text("服务器监控"), func(sub *ui.Menu) {
			sub.Item(locales.Text("管理服务器监控")).Disabled(true)
		}).Disabled(true)
		if m.Item(locales.Text("在数据库或模式中查找…")).Shortcut(ui.Ctrl|ui.Shift, ui.KeyF).Disabled(!s.objectWorkspace.CanSearchObjects(s.sessions.Current().ProfileID)).Chosen() {
			s.openObjectSearch()
		}
		m.Separator()
		if m.Item(locales.Text("历史日志…")).Shortcut(ui.Ctrl, ui.KeyL).Disabled(!s.sqlWorkspace.HistoryAvailable()).Chosen() {
			s.sqlWorkspace.OpenHistory(0, s.openHistoryQuery)
		}
		m.Separator()
		if m.Item(locales.Text("选项…")).Chosen() {
			s.openSettings()
		}
	case "窗口":
		s.documentMenuCommands(section, m)
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
	mode = theme.ParseMode(string(mode))
	if !s.saveThemePreference(theme.ModePreferenceKey, string(mode)) {
		return
	}
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
	palette := theme.Colors(c)

	actions := []toolAction{
		{"连接", "connection", palette.Blue},
		{"新建查询", "new-query", palette.Blue},
		{"表", "table", palette.Blue},
		{"视图", "view", palette.Blue},
		{"函数", "function", palette.Blue},
		{"用户", "user", palette.Gold},
		{"其它", "tools", palette.Blue},
		{"查询", "query", palette.Blue},
		{"备份", "backup", palette.Blue},
		{"自动运行", "automation", palette.Green},
		{"模型", "model", palette.Gold},
	}
	ui.Row(c).Height(theme.RowHeight(c, theme.ToolbarHeight)).AlignItems(ui.Center).Padding(1, 4).Gap(1).
		Background(palette.Toolbar).Children(func() {
		ui.Toolbar(c, func() {

			kind := s.sessions.Current().Kind
			if profile, ok := s.currentProfile(); ok {
				kind = profile.Kind
			}
			for _, action := range actions {
				if kind == connections.TiDB && action.label == "函数" {
					continue
				}
				if s.profileMode {
					if kind == connections.SQLite && (action.label == "函数" || action.label == "用户" || action.label == "其它") {
						continue
					}
					if kind.IsPostgreSQLFamily() && action.label == "用户" {
						continue
					}
				}

				if kind == connections.Cassandra {
					if action.label == "用户" || action.label == "其它" {
						continue
					}
					if action.label == "函数" {
						action.label = "类型"
					}
				}
				if kind == connections.Redis {
					if action.label == "视图" || action.label == "函数" || action.label == "用户" || action.label == "其它" {
						continue
					}
					if action.label == "表" {
						action.label = "键"
					}
				}
				if kind.IsRESTSearch() && kind != connections.Solr {
					if action.label == "函数" || action.label == "用户" || action.label == "其它" {
						continue
					}
					switch action.label {
					case "表":
						action.label = "索引"
					case "视图":
						action.label = "别名"
					case "查询":
						action.label = "数据流"
					}
				}
				if kind == connections.Qdrant {
					switch action.label {
					case "表":
						action.label = "集合信息"
					case "视图":
						action.label = "向量点"
					case "函数":
						action.label = "集合别名"
					case "其它", "用户":
						continue
					}
				}
				if kind == connections.Meilisearch {
					switch action.label {
					case "表":
						action.label = "索引信息"
					case "视图":
						action.label = "索引设置"
					case "函数":
						action.label = "索引统计"
					case "其它":
						action.label = "文档"
					case "用户":
						continue
					}
				}
				if kind == connections.CouchDB {
					switch action.label {
					case "表":
						action.label = "文档"
					case "视图":
						action.label = "数据库信息"
					case "函数":
						action.label = "设计文档"
					case "查询":
					default:
						continue
					}
				}
				if kind == connections.Solr {
					if action.label == "函数" || action.label == "用户" || action.label == "其它" {
						continue
					}
					switch action.label {
					case "表":
						action.label = "Core"
					case "视图":
						action.label = "集合"
					case "查询":
						action.label = "别名"
					}
				}
				if kind == connections.MongoDB {
					if action.label == "函数" || action.label == "用户" || action.label == "其它" {
						continue
					}
					if action.label == "表" {
						action.label = "集合"
					}
				}
				if action.label == "表" || action.label == "集合" {
					ui.Divider(c).Margin(4).Background(palette.Border)
				}
				item := action
				active := s.currentObjectKind() == item.label && s.currentTab().kind == tabObjects
				tip := locales.Text(item.label)
				if !s.toolbarActionAvailable(item.label) {
					reason := "invalid_target"
					if item.label == "自动运行" || item.label == "模型" {
						reason = "not_implemented"
					} else if s.sessions.Current().Status != "已连接" {
						reason = "disconnected"
					} else if w := s.sessions.Current(); w.Database != "" && s.sessions.DatabaseOpened(w.ProfileID, w.Database) {
						reason = "unsupported"
					}
					tip += " · " + theme.AvailabilityText(contract.Availability{Reason: reason})
				}
				button := ui.Button(c, "").Label(locales.Text(item.label)).Tooltip(tip).
					MinWidth(theme.LabelWidth(c, locales.Text(item.label), theme.ToolbarItemWidth)).Height(theme.RowHeight(c, theme.ToolbarItemHeight)).Padding(3, 2).Radius(theme.ControlRadius).
					Disabled(!s.toolbarActionAvailable(item.label)).Border(0, ui.Transparent).
					Children(func() {
						ui.Column(c).Center().Gap(4).Children(func() {
							if item.label == "连接" {
								theme.ConnectionActionIcon(c, theme.ToolbarIconSize)
							} else {
								theme.Icon(c, item.icon, theme.ToolbarIconSize, item.color)
							}
							ui.Text(c, locales.Text(item.label)).FontSize(theme.Font(c, theme.FontSize)).SingleLine()
						})
					})
				styleToolbarButton(c, button, active)
				if item.label == "其它" || item.label == "备份" {
					button.Menu(func(m *ui.Menu) { s.toolbarMenu(c.Services(), item.label, m) })
				}
				if button.Clicked() {
					if item.label != "其它" && item.label != "备份" {
						s.dispatchToolbar(item.label, c.Services())
					}
				}
			}
		}).Label(locales.Text("主工具栏")).Grow(1).MinWidth(0).Padding(0).Gap(1).Background(palette.Toolbar)
		// Keep the inspector toggle available even when toolbar actions overflow.
		info := ui.Button(c, "").Label(locales.Text("信息")).Tooltip(locales.Text("显示或隐藏信息面板")).
			MinWidth(theme.LabelWidth(c, locales.Text("信息"), 48)).Height(theme.RowHeight(c, theme.ToolbarItemHeight)).Padding(3, 2).Radius(theme.ControlRadius).Border(0, ui.Transparent).Children(func() {
			ui.Column(c).Center().Gap(4).Children(func() {
				theme.Icon(c, "info", theme.ToolbarIconSize, palette.Blue)
				ui.Text(c, locales.Text("信息")).FontSize(theme.Font(c, theme.FontSize))
			})
		})
		styleToolbarButton(c, info, false)
		if info.Clicked() {
			s.clickToolbar("信息")
		}
	})
}

// Disabled commands keep their place in the native toolbar and its overflow
// menu, but never announce a successful operation for an unsupported feature.
func (s *State) toolbarActionAvailable(action string) bool {
	switch action {
	case "集合信息", "向量点", "集合别名", "文档", "索引信息", "索引设置", "索引统计", "数据库信息", "设计文档":
		w := s.sessions.Current()
		return w.Status == "已连接" && w.Database != "" && s.sessions.DatabaseOpened(w.ProfileID, w.Database) && s.objectCapabilitiesFor(w.ProfileID).Metadata
	case "连接", "新建查询", "Core", "索引", "别名", "数据流", "键", "集合", "表", "视图", "信息":
		return true
	case "类型":
		w := s.sessions.Current()
		return w.Kind == connections.Cassandra && w.Status == "已连接" && w.Database != "" && s.sessions.DatabaseOpened(w.ProfileID, w.Database) && s.objectCapabilitiesFor(w.ProfileID).Metadata
	case "函数":
		if s.sessions.Current().Kind == connections.TiDB {
			return false
		}
		if !s.profileMode {
			return true
		}
		fallthrough
	case "其它":
		w := s.sessions.Current()
		caps := s.objectCapabilitiesFor(w.ProfileID)
		return w.Status == "已连接" && w.Database != "" && s.sessions.DatabaseOpened(w.ProfileID, w.Database) && (caps.DesignTables || (w.Kind.IsPostgreSQLFamily() || w.Kind == connections.SQLServer || w.Kind == connections.Oracle || w.Kind == connections.Dameng || w.Kind == connections.DuckDB) && caps.Metadata)
	case "查询":
		return s.profileMode && s.selectedProfileID > 0 && !s.sqlWorkspace.PersistenceLoading()
	case "用户":
		return s.profileMode && s.objectWorkspace.CanAccounts(s.sessions.Current().ProfileID)
	case "备份":
		w := s.sessions.Current()
		return s.profileMode && w.Database != "" && s.objectWorkspace.CanBackup(w.ProfileID)
	default:
		return false
	}
}

func styleToolbarButton(c *ui.Context, button ui.Element, selected bool) {
	palette := theme.Colors(c)

	background := palette.Toolbar
	if selected {
		background = palette.ToolbarSelected
		button.Border(theme.BorderWidth, palette.Border)
	}
	if button.Hovered() {
		background = palette.SurfaceHover
	}
	if button.Pressed() {
		background = palette.SurfacePressed
	}
	button.Background(background)
}

func (s *State) mysqlWorkbenchStatus() string {
	if s.sessions.Current().Status == "" {
		return "未连接"
	}
	return s.sessions.Current().Status
}

func (s *State) statusBar(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, theme.StatusBarHeight)).Padding(1, 6).AlignItems(ui.Center).Gap(10).
		Background(palette.Menu).BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).Children(func() {
		ui.Text(c, locales.Text(s.status)).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Text).SingleLine().Grow(1).MinWidth(0)
		if s.initialSQLCleanup.err != nil && len(s.initialSQLCleanup.candidates) > 0 {
			if ui.Button(c, locales.Text("重试清理初始 SQL")).Height(theme.RowHeight(c, theme.StatusBarHeight)-2).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(0, 4).Disabled(s.sessions.HasPendingConnections()).Clicked() {
				_ = s.RetryInitialSQLCleanup()
			}
		}
		if s.profileMode {
			ui.Text(c, fmt.Sprintf("%d %s", len(s.navigation.Profiles()), locales.Text("个连接档案"))).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
			ui.Text(c, "●  "+locales.Text(s.mysqlWorkbenchStatus())).FontSize(theme.Font(c, 11)).TextColor(palette.Blue)
			return
		}
		ui.Text(c, "5 "+locales.Text("个模拟连接")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		ui.Text(c, "●  UI Preview").FontSize(theme.Font(c, 11)).TextColor(palette.Blue)
	})
}

func (s *State) inspectorVisible() bool {
	return s.inspectorOn && s.currentTab().kind != tabSQL && s.currentTab().kind != tabSettings
}
