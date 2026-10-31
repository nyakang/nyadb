package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The connection editor is a native MyGo view. Layout and tab order are based
// on observed Navicat 18 UI behavior, not on the original application's code.
func (s *State) View(c *ui.Context, width, height float32) {
	if !s.active {
		return
	}
	s.consumeMySQLConnectionTest()
	applyConnectionEditorFormTheme(c)

	palette := theme.Colors(c)
	w, h := max(width, float32(520)), max(height, float32(420))
	footerHeight := connectionEditorFooterHeight(c, w)
	ui.Column(c).Width(w).Height(h).Gap(0).Background(palette.Toolbar).Children(func() {
		ui.Row(c).Height(h - footerHeight).Gap(0).Children(func() {
			contentWidth := w
			if s.connectionDraft.ID != 0 && s.connectionDraft.Kind == connections.MySQL &&
				s.connectionProfilesVisible {
				s.connectionConfigurationsSidebar(c, h-footerHeight)
				contentWidth -= 258
			}
			ui.Column(c).Width(contentWidth).Height(h - footerHeight).Gap(0).Children(func() {
				s.connectionEditorTabs(c)
				ui.Box(c).Height(1).Background(palette.Border)
				// Forms retain their normal metrics while small windows scroll the
				// body independently of the tabs and confirmation controls.
				formWidth := max(contentWidth, float32(520))
				ui.ScrollBoth(c).Key(s.connectionTabNames()[s.connectionEditorTab]).
					Label(locales.Text("连接设置内容")).Grow(1).FillWidth().Children(func() {
					ui.Column(c).Width(formWidth).Gap(0).AlignItems(ui.Center).Children(func() {
						s.connectionConfigurationCaption(c)
						s.connectionEditorDiagram(c)
						if s.connectionEditorTab == 0 {
							s.connectionEditorGeneral(c, formWidth)
						} else {
							s.connectionEditorOtherTab(c, formWidth)
						}
						if s.connectionError != "" {
							ui.Text(c, s.connectionError).Width(connectionFormWidth(formWidth)).FontSize(theme.Font(c, 12)).TextColor(palette.Error).Padding(15, 0)
						}
						s.connectionTestStatus(c)
					})
				})
			})
		})
		ui.Box(c).Height(1).Background(palette.Border)
		s.connectionEditorFooter(c, w)
	})
	s.mysqlInitialQueryModal(c)
}

func connectionEditorTabNames(kind connections.Kind) []string {
	switch kind {
	case connections.SQLite:
		return []string{"常规", "高级", "附加的数据库", "HTTP", "备注"}
	case connections.PostgreSQL:
		return []string{"常规", "高级", "SSL", "SSH", "HTTP", "备注"}
	default:
		return []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"}
	}
}

func (s *State) connectionTabNames() []string {
	if s.connectionDraft.Kind == connections.MySQL && s.mysqlCompatibilityVisible {
		return []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "兼容性", "备注"}
	}
	return connectionEditorTabNames(s.connectionDraft.Kind)
}

func (s *State) connectionEditorTabs(c *ui.Context) {
	palette := theme.Colors(c)

	names := s.connectionTabNames()
	if s.connectionEditorTab >= len(names) {
		s.connectionEditorTab = 0
	}
	ui.ScrollHorizontal(c).Height(theme.RowHeight(c, 29)).FillWidth().Gap(0).Background(palette.Toolbar).Children(func() {
		tabs := ui.TabsBase(c, &s.connectionEditorTab, len(names))
		tabs.List.Gap(0).Children(func() {
			for i, name := range names {
				item := tabs.Tab(i).Height(theme.RowHeight(c, 28)).Padding(4, 12).Border(1, palette.Border)
				if i == s.connectionEditorTab {
					item.Background(palette.Toolbar)
				}
				item.Children(func() {
					ui.Text(c, locales.Text(name)).FontSize(theme.Font(c, 12)).SingleLine()
				})
			}
		})
	})
}

func (s *State) connectionEditorDiagram(c *ui.Context) {
	palette := theme.Colors(c)

	// The tunnel endpoint appears across tabs, mirroring the original
	// connection editor; all graphics are independently authored for NyaDB.
	if s.connectionDraft.Kind == connections.MySQL &&
		s.connectionDraft.MySQLAdvanced.HTTP.Enabled {
		ui.Row(c).MinHeight(91).Gap(7).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 36, palette.Muted)
				ui.Text(c, "NyaDB").FontSize(theme.Font(c, 11)).SingleLine()
			})
			ui.Box(c).Width(75).Height(2).Background(palette.Border)
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "ssh-server", 36, palette.Muted)
				ui.Text(c, locales.Text("HTTP 服务器")).FontSize(theme.Font(c, 11)).SingleLine()
			})
			ui.Box(c).Width(75).Height(2).Background(palette.Border)
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "database", 36, palette.Muted)
				ui.Text(c, locales.Text("数据库")).FontSize(theme.Font(c, 11)).SingleLine()
			})
		})
		return
	}
	// The original inserts an SSH server node only when the tunnel is
	// enabled. These icons are independently authored NyaDB assets.
	if s.connectionDraft.Kind == connections.MySQL &&
		s.connectionDraft.MySQLAdvanced.SSH.Enabled {
		ui.Row(c).MinHeight(91).Gap(7).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 36, palette.Muted)
				ui.Text(c, "NyaDB").FontSize(theme.Font(c, 11)).SingleLine()
			})
			ui.Box(c).Width(75).Height(2).Background(palette.Border)
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "ssh-server", 36, palette.Muted)
				ui.Text(c, locales.Text("SSH 服务器")).FontSize(theme.Font(c, 11)).SingleLine()
			})
			ui.Box(c).Width(75).Height(2).Background(palette.Border)
			ui.Column(c).MinWidth(theme.Font(c, 65)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "database", 36, palette.Muted)
				ui.Text(c, locales.Text("数据库")).FontSize(theme.Font(c, 11)).SingleLine()
			})
		})
		return
	}
	ui.Row(c).MinHeight(91).Gap(8).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Column(c).MinWidth(theme.Font(c, 42)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
			theme.Icon(c, "connection", 37, palette.Muted)
			ui.Text(c, "NyaDB").FontSize(theme.Font(c, 11)).SingleLine()
		})
		ui.Box(c).Width(112).Height(2).Background(palette.Border)
		ui.Column(c).MinWidth(theme.Font(c, 42)).Shrink(0).Gap(3).AlignItems(ui.Center).Children(func() {
			theme.Icon(c, "database", 37, palette.Muted)
			ui.Text(c, locales.Text("数据库")).FontSize(theme.Font(c, 11)).SingleLine()
		})
	})
}

func (s *State) connectionEditorGeneral(c *ui.Context, width float32) {
	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	p := &s.connectionDraft
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		s.editorInputRow(c, "连接名称：", "连接名称", &p.Name, labelWidth, fieldWidth)
		ui.Box(c).Height(theme.RowHeight(c, 31))
		if p.Kind == connections.SQLite {
			s.editorSQLiteFields(c, labelWidth, fieldWidth)
			return
		}
		s.editorInputRow(c, "主机：", "主机", &p.Host, labelWidth, fieldWidth)
		s.editorInputRow(c, "端口：", "端口", &s.connectionPort, labelWidth, min(fieldWidth, float32(80)))
		if p.Kind == connections.PostgreSQL {
			s.editorInputRow(c, "初始数据库：", "初始数据库", &p.Database, labelWidth, fieldWidth)
		}
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, min(fieldWidth, float32(200)))
		connectionFormRow(c).Children(func() {
			ui.Text(c, locales.Text("密码：")).Width(labelWidth).FontSize(theme.Font(c, 12))
			connectionFormTextInput(c, &s.connectionSecret, "密码", min(fieldWidth, float32(200)), false, true)
		})
		ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(labelWidth)
			connectionFormCheckbox(c, &s.connectionSavePassword, "保存密码",
				s.connectionDraft.Kind != connections.MySQL || s.credentialVault == nil)
		})
	})
}

func (s *State) editorInputRow(
	c *ui.Context, caption, label string, target *string, labelWidth, fieldWidth float32,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, locales.Text(caption)).Width(labelWidth).FontSize(theme.Font(c, 12))
		input := connectionFormTextInput(c, target, label, fieldWidth,
			s.connectionDraft.ID != 0 && s.connectionActiveConfig != 0 && label == "连接名称", false)
		if label == "连接名称" && s.connectionActiveConfig == 0 && s.connectionNameFocusPending {
			// Focus just once when the editor opens. Continuous AutoFocus on
			// rerender steals keyboard focus from the configuration sidebar.
			input.AutoFocus()
			s.connectionNameFocusPending = false
		}
	})
}

func (s *State) editorSQLiteFields(c *ui.Context, labelWidth, fieldWidth float32) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, 27)).Gap(0).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(labelWidth)
		selected := true
		connectionFormCheckbox(c, &selected, "现有数据库文件", true)
	})
	connectionFormRow(c).Children(func() {
		ui.Text(c, locales.Text("数据库文件：")).Width(labelWidth).FontSize(theme.Font(c, 12))
		connectionFormTextInput(c, &s.connectionDraft.Database, "数据库文件", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
		ui.Box(c).Width(connectionFormBrowseGap)
		connectionFormBrowse(c, "浏览文件", s.sqliteFilePicker == nil, s.sqliteFilePicker)
	})
	ui.Row(c).Gap(0).Children(func() {
		ui.Box(c).Width(labelWidth)
		ui.Text(c, locales.Text("仅支持选择现有数据库；不会擅自创建或修改数据库文件。")).
			FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
	})
}

func (s *State) connectionEditorOtherTab(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	names := s.connectionTabNames()
	tab := names[s.connectionEditorTab]
	if tab == "备注" {
		s.connectionNotesEditor(c, width)
		return
	}
	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	if s.connectionDraft.Kind == connections.MySQL {
		switch tab {
		case "高级":
			s.connectionMySQLAdvanced(c, width)
			return
		case "兼容性":
			s.connectionMySQLCompatibility(c, width)
			return
		case "数据库":
			_, height := c.Size()
			s.connectionMySQLDatabases(c, width, height)
			return
		case "SSL":
			s.connectionMySQLSSL(c, width)
			return
		case "SSH":
			s.connectionMySQLSSH(c, width)
			return
		case "HTTP":
			s.connectionMySQLHTTP(c, width)
			return
		}
	}
	ui.Column(c).Width(formWidth).Gap(12).Children(func() {
		// The profile model already has a database field. Keeping it on MySQL's
		// database tab avoids adding a field not present on its observed General tab.
		if tab == "数据库" {
			s.editorInputRow(c, "默认数据库：", "默认数据库", &s.connectionDraft.Database,
				labelWidth, formWidth-labelWidth)
			ui.Text(c, locales.Text("数据库筛选与连接后数据库列表将在驱动接入后实现。")).
				FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
			return
		}
		ui.Text(c, tab+" 配置尚未实现").FontSize(theme.Font(c, 13)).TextColor(palette.Muted)
		ui.Text(c, locales.Text("此页仅保留原版导航位置；相关设置不会被静默保存或应用。")).
			FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
	})
}

func connectionEditorFooterHeight(c *ui.Context, width float32) float32 {
	if width < theme.Font(c, 497)+53 {
		return 2*theme.RowHeight(c, connectionFooterControlHeight) + 24
	}
	return theme.RowHeight(c, 43)
}

func (s *State) connectionEditorFooter(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	ui.Row(c).Wrap().Height(connectionEditorFooterHeight(c, width)-1).Padding(5, 9).Gap(7).AlignItems(ui.Center).
		Background(palette.Toolbar).Children(func() {
		// Show/hide the native editing configuration sidebar.
		if ui.Button(c, "▣").Width(theme.Font(c, 26)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
			Background(palette.Header).Border(1, palette.Border).
			Disabled(s.connectionDraft.ID == 0 || s.connectionDraft.Kind != connections.MySQL).
			Label("切换连接配置文件侧栏").Clicked() {
			s.connectionProfilesVisible = !s.connectionProfilesVisible
		}
		ui.Button(c, "URI").Width(theme.Font(c, 76)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
			Background(palette.Header).Border(1, palette.Border).
			Disabled(true).Label(locales.Text("URI 导入尚未支持"))
		if s.connectionTesting {
			if ui.Button(c, locales.Text("取消测试")).Width(theme.Font(c, 155)).Height(theme.RowHeight(c, connectionFooterControlHeight)).Background(palette.Header).Border(1, palette.Border).Clicked() {
				s.cancelMySQLConnectionTest()
				s.connectionTestMessage = "连接测试已取消"
			}
		} else if ui.Button(c, locales.Text("测试连接")).Width(theme.Font(c, 155)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
			Background(palette.Header).Border(1, palette.Border).
			Disabled(s.connectionDraft.Kind != connections.MySQL).Label(locales.Text("测试当前 MySQL 配置")).Clicked() {
			s.startMySQLConnectionTest(c.Services())
		}
		ui.Box(c).Grow(1)
		if s.connectionDraft.ID == 0 {
			if ui.Button(c, locales.Text("上一步")).Width(theme.Font(c, 80)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
				Background(palette.Header).Border(1, palette.Border).
				Disabled(s.connectionDraft.ID != 0).Clicked() {
				s.Back()
			}
		}
		if ui.Button(c, locales.Text("确定")).Width(theme.Font(c, 80)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
			Background(palette.Header).Border(1, palette.Border).Label("保存连接").Clicked() {
			s.Save()
		}
		if ui.Button(c, locales.Text("取消")).Width(theme.Font(c, 80)).Height(theme.RowHeight(c, connectionFooterControlHeight)).
			Background(palette.Header).Border(1, palette.Border).
			Label("取消连接向导").Clicked() {
			s.Cancel()
		}
	})
}

// connectionNotesEditor reproduces the full-width multiline Notes page.
// The draft is persisted only when the connection editor's Confirm action
// succeeds; Cancel discards it along with the other draft changes.
func (s *State) connectionNotesEditor(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	_, height := c.Size()
	formWidth := max(float32(480), width-40)
	editorHeight := max(float32(180), min(float32(475), height-226))
	ui.Column(c).Width(formWidth).Gap(4).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 22)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "备注：*").FontSize(theme.Font(c, 12))
		})
		ui.TextArea(c, &s.connectionDraft.Notes).
			Label("连接备注").
			Width(formWidth).Height(editorHeight).
			Padding(7, 6).FontSize(theme.Font(c, 12)).
			Background(palette.App).Border(1, palette.App)
		ui.Row(c).Height(18).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "* 更改将应用于所有连接配置文件。").
				FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		})
	})
}
