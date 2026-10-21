package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Shared chrome colors, measured from Navicat 18 running under isolated Xvfb.
// Keep them local to the connection editor rather than changing the NyaDB
// workbench palette or per-tab business controls.
var (
	connectionEditorSurface = ui.Hex("#31363b")
	connectionEditorTabsBG  = ui.Hex("#30353a")
	connectionEditorBorder  = ui.Hex("#23272a")
	connectionEditorButton  = ui.Hex("#3f4448")
	connectionDiagramLine   = ui.Hex("#76787a")
)

// The connection editor is a native MyGo view. Layout and tab order are based
// on observed Navicat 18 UI behavior, not on the original application's code.
func (s *State) connectionEditor(c *ui.Context) {
	w, h := c.Size()
	if s.showConnectionWindow == nil {
		w = min(float32(760), w-96)
		h = min(float32(630), h-96)
	}
	if w < 520 {
		w = 520
	}
	if h < 420 {
		h = 420
	}
	ui.Column(c).Width(w).Height(h).Gap(0).Background(connectionEditorSurface).Children(func() {
		s.connectionEditorTabs(c)
		ui.Box(c).Height(1).Background(connectionEditorBorder)
		ui.Column(c).Grow(1).Gap(0).AlignItems(ui.Center).Children(func() {
			s.connectionEditorDiagram(c)
			if s.connectionEditorTab == 0 {
				s.connectionEditorGeneral(c, w)
			} else {
				s.connectionEditorOtherTab(c, w)
			}
			if s.connectionError != "" {
				ui.Text(c, s.connectionError).FontSize(12).TextColor(theme.Gold).Padding(15, 0)
			}
		})
		ui.Box(c).Height(1).Background(connectionEditorBorder)
		s.connectionEditorFooter(c)
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
	names := s.connectionTabNames()
	if s.connectionEditorTab >= len(names) {
		s.connectionEditorTab = 0
	}
	ui.Row(c).Height(29).Gap(0).Background(connectionEditorTabsBG).Children(func() {
		tabs := ui.TabsBase(c, &s.connectionEditorTab, len(names))
		tabs.List.Gap(0).Children(func() {
			for i, name := range names {
				item := tabs.Tab(i).Height(28).Padding(4, 12).Border(1, connectionEditorBorder)
				if i == s.connectionEditorTab {
					item.Background(connectionEditorSurface)
				}
				item.Children(func() {
					ui.Text(c, name).FontSize(12).SingleLine()
				})
			}
		})
		ui.Box(c).Grow(1)
	})
}

func (s *State) connectionEditorDiagram(c *ui.Context) {
	// The tunnel endpoint appears across tabs, mirroring the original
	// connection editor; all graphics are independently authored for NyaDB.
	if s.connectionDraft.Kind == connections.MySQL &&
		s.connectionDraft.MySQLAdvanced.HTTP.Enabled {
		ui.Row(c).Height(91).Gap(7).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 36, theme.Muted)
				ui.Text(c, "NyaDB").FontSize(11)
			})
			ui.Box(c).Width(75).Height(2).Background(connectionDiagramLine)
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "ssh-server", 36, theme.Muted)
				ui.Text(c, "HTTP 服务器").FontSize(11)
			})
			ui.Box(c).Width(75).Height(2).Background(connectionDiagramLine)
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "database", 36, theme.Muted)
				ui.Text(c, "数据库").FontSize(11)
			})
		})
		return
	}
	// The original inserts an SSH server node only when the tunnel is
	// enabled. These icons are independently authored NyaDB assets.
	if s.connectionDraft.Kind == connections.MySQL &&
		s.connectionDraft.MySQLAdvanced.SSH.Enabled {
		ui.Row(c).Height(91).Gap(7).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 36, theme.Muted)
				ui.Text(c, "NyaDB").FontSize(11)
			})
			ui.Box(c).Width(75).Height(2).Background(connectionDiagramLine)
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "ssh-server", 36, theme.Muted)
				ui.Text(c, "SSH 服务器").FontSize(11)
			})
			ui.Box(c).Width(75).Height(2).Background(connectionDiagramLine)
			ui.Column(c).Width(65).Gap(3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "database", 36, theme.Muted)
				ui.Text(c, "数据库").FontSize(11)
			})
		})
		return
	}
	ui.Row(c).Height(91).Gap(8).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Column(c).Width(42).Gap(3).AlignItems(ui.Center).Children(func() {
			theme.Icon(c, "connection", 37, theme.Muted)
			ui.Text(c, "NyaDB").FontSize(11)
		})
		ui.Box(c).Width(112).Height(2).Background(connectionDiagramLine)
		ui.Column(c).Width(42).Gap(3).AlignItems(ui.Center).Children(func() {
			theme.Icon(c, "database", 37, theme.Muted)
			ui.Text(c, "数据库").FontSize(11)
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
		ui.Box(c).Height(31)
		if p.Kind == connections.SQLite {
			s.editorSQLiteFields(c, labelWidth, fieldWidth)
			return
		}
		s.editorInputRow(c, "主机：", "主机", &p.Host, labelWidth, fieldWidth)
		s.editorInputRow(c, "端口：", "端口", &s.connectionPort, labelWidth, min(fieldWidth, float32(90)))
		if p.Kind == connections.PostgreSQL {
			s.editorInputRow(c, "初始数据库：", "初始数据库", &p.Database, labelWidth, fieldWidth)
		}
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, min(fieldWidth, float32(205)))
		connectionFormRow(c).Children(func() {
			ui.Text(c, "密码：").Width(labelWidth).FontSize(12)
			connectionFormTextInput(c, &s.connectionSecret, "密码", min(fieldWidth, float32(205)), false, true)
		})
		ui.Row(c).Height(28).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(labelWidth)
			save := false
			ui.Checkbox(c, &save, "保存密码").Disabled(true).
				Label("保存密码（需要系统密钥库）")
		})
		ui.Row(c).Gap(0).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, "密码仅在当前窗口内保留；安全保存将在密钥库接入后开放。").
				FontSize(11).TextColor(theme.Muted)
		})
	})
}

func (s *State) editorInputRow(
	c *ui.Context, caption, label string, target *string, labelWidth, fieldWidth float32,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, caption).Width(labelWidth).FontSize(12)
		input := connectionFormTextInput(c, target, label, fieldWidth, false, false)
		if label == "连接名称" {
			// The observed original opens the General tab with this field focused.
			input.AutoFocus()
		}
	})
}

func (s *State) editorSQLiteFields(c *ui.Context, labelWidth, fieldWidth float32) {
	ui.Row(c).Height(27).Gap(0).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(labelWidth)
		selected := true
		ui.Checkbox(c, &selected, "现有数据库文件").Disabled(true)
	})
	ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "数据库文件：").Width(labelWidth).FontSize(12)
		ui.TextInput(c, &s.connectionDraft.Database).Label("数据库文件").
			Width(fieldWidth-36).Height(30).Padding(2, 10)
		if ui.Button(c, "…").Label("浏览文件").Width(35).
			Disabled(s.sqliteFilePicker == nil).Clicked() {
			s.sqliteFilePicker()
		}
	})
	ui.Row(c).Gap(0).Children(func() {
		ui.Box(c).Width(labelWidth)
		ui.Text(c, "仅支持选择现有数据库；不会擅自创建或修改数据库文件。").
			FontSize(11).TextColor(theme.Muted)
	})
}

func (s *State) connectionEditorOtherTab(c *ui.Context, width float32) {
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
			ui.Text(c, "数据库筛选与连接后数据库列表将在驱动接入后实现。").
				FontSize(11).TextColor(theme.Muted)
			return
		}
		ui.Text(c, tab+" 配置尚未实现").FontSize(13).TextColor(theme.Muted)
		ui.Text(c, "此页仅保留原版导航位置；相关设置不会被静默保存或应用。").
			FontSize(11).TextColor(theme.Muted)
	})
}

func (s *State) connectionEditorFooter(c *ui.Context) {
	ui.Row(c).Height(42).Padding(5, 9).Gap(7).AlignItems(ui.Center).
		Background(connectionEditorSurface).Children(func() {
		// The original reserves the first slot for the profile-list toggle.
		// The edit-profile sidebar is not implemented yet.
		ui.Button(c, "▣").Width(26).Height(26).
			Background(connectionEditorButton).Border(1, connectionEditorBorder).
			Disabled(true).Label("连接配置文件列表（尚未实现）")
		ui.Button(c, "URI").Width(76).Height(26).
			Background(connectionEditorButton).Border(1, connectionEditorBorder).
			Disabled(true).Label("URI 导入尚未支持")
		ui.Button(c, "测试连接").Width(155).Height(26).
			Background(connectionEditorButton).Border(1, connectionEditorBorder).Disabled(true).
			Label("测试连接待数据库驱动接入")
		ui.Box(c).Grow(1)
		if ui.Button(c, "上一步").Width(80).Height(26).
			Background(connectionEditorButton).Border(1, connectionEditorBorder).
			Disabled(s.connectionDraft.ID != 0).Clicked() {
			s.connectionStep = 0
			s.connectionEditorTab = 0
			s.connectionSecret = ""
			s.clearMySQLSSLPassphrase()
			s.clearMySQLSSHSecrets()
			s.clearMySQLHTTPSecrets()
			s.clearMySQLInitialQuery()
			s.connectionError = ""
			s.updateConnectionWindowTitle()
		}
		if ui.PrimaryButton(c, "确定").Width(80).Height(26).
			Label("保存连接").Clicked() {
			s.saveConnectionDraft()
		}
		if ui.Button(c, "取消").Width(80).Height(26).
			Background(connectionEditorButton).Border(1, connectionEditorBorder).
			Label("取消连接向导").Clicked() {
			s.closeConnectionDialog()
		}
	})
}
