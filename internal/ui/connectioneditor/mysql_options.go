package connectioneditor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The four timeouts and session limit are bound as strings so users can edit
// incomplete numbers naturally. Conversion happens only when saving.
const (
	mysqlKeepAlive = iota
	mysqlConnectTimeout
	mysqlReadTimeout
	mysqlWriteTimeout
	mysqlSessionLimit
)

func (s *State) initMySQLAdvancedInputs() {
	a := s.connectionDraft.MySQLAdvanced
	if a == (connections.MySQLAdvanced{}) {
		a = connections.DefaultMySQLAdvanced()
		s.connectionDraft.MySQLAdvanced = a
	}
	for i, n := range []int{
		a.KeepAliveSeconds, a.ConnectTimeoutSeconds, a.ReadTimeoutSeconds,
		a.WriteTimeoutSeconds, a.MaxSessions,
	} {
		s.mysqlAdvancedNumbers[i] = strconv.Itoa(n)
	}
	s.mysqlCompatibilityVisible = false
}

// advanceDraft validates the disabled as well as enabled numeric fields, so
// toggling a checkbox never accidentally destroys the user's chosen value.
func (s *State) advanceDraft(p *connections.Profile) error {
	if p.Kind != connections.MySQL {
		return nil
	}
	labels := []string{"保持连接间隔", "连接超时", "读取超时", "写入超时", "限制连接会话"}
	maxima := []int{86400, 86400, 86400, 86400, 10000}
	values := [5]int{}
	for i, raw := range s.mysqlAdvancedNumbers {
		num, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || num < 1 || num > maxima[i] {
			return fmt.Errorf("%s必须是 1–%d 之间的整数", labels[i], maxima[i])
		}
		values[i] = num
	}
	p.MySQLAdvanced.KeepAliveSeconds = values[mysqlKeepAlive]
	p.MySQLAdvanced.ConnectTimeoutSeconds = values[mysqlConnectTimeout]
	p.MySQLAdvanced.ReadTimeoutSeconds = values[mysqlReadTimeout]
	p.MySQLAdvanced.WriteTimeoutSeconds = values[mysqlWriteTimeout]
	p.MySQLAdvanced.MaxSessions = values[mysqlSessionLimit]
	return nil
}

func (s *State) connectionMySQLAdvanced(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	a := &s.connectionDraft.MySQLAdvanced
	// The shared 31-DIP rhythm leaves room around 26-DIP native controls.
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			ui.Text(c, "设置位置：").Width(labelWidth).FontSize(theme.Font(c, 12))
			connectionFormTextInput(c, &a.SettingsLocation, "设置位置", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "选择设置位置", s.mysqlSettingsPicker == nil, s.mysqlSettingsPicker)
		})
		connectionFormRow(c).Children(func() {
			ui.Text(c, "客户端驱动程序：").Width(labelWidth).FontSize(theme.Font(c, 12))
			connectionFormSelect(c, &a.ClientDriver, []string{"默认", "旧版"}, "客户端驱动程序", fieldWidth, false)
		})
		connectionFormRow(c).Children(func() {
			ui.Text(c, "客户端字符集：").Width(labelWidth).FontSize(theme.Font(c, 12))
			connectionFormSelect(c, &a.Charset, connections.MySQLCharsets, "客户端字符集", fieldWidth, false)
		})
		s.mysqlAdvancedNumberRow(c, &a.KeepAlive, "保持连接间隔（秒）：",
			&s.mysqlAdvancedNumbers[mysqlKeepAlive], labelWidth)
		s.mysqlAdvancedNumberRow(c, &a.ConnectTimeout, "连接超时（秒）：",
			&s.mysqlAdvancedNumbers[mysqlConnectTimeout], labelWidth)
		s.mysqlAdvancedNumberRow(c, &a.ReadTimeout, "读取超时（秒）：",
			&s.mysqlAdvancedNumbers[mysqlReadTimeout], labelWidth)
		s.mysqlAdvancedNumberRow(c, &a.WriteTimeout, "写入超时（秒）：",
			&s.mysqlAdvancedNumbers[mysqlWriteTimeout], labelWidth)
		s.mysqlAdvancedToggle(c, &a.Compression, "使用压缩")
		s.mysqlAdvancedToggle(c, &a.AutoConnect, "自动连接")
		connectionFormRow(c).Children(func() {
			connectionFormCheckboxCompact(c, &a.NamedPipe, "使用命名管道，套接字：", false).Width(labelWidth)
			connectionFormTextInput(c, &a.PipeName, "命名管道或套接字", 200, !a.NamedPipe, false)
		})
		s.mysqlAdvancedNumberRow(c, &a.LimitSessions, "限制连接会话：",
			&s.mysqlAdvancedNumbers[mysqlSessionLimit], labelWidth)
		connectionFormRow(c).Children(func() {
			ui.Text(c, "初始查询：").Width(labelWidth).FontSize(theme.Font(c, 12))
			if connectionFormTextInput(c, &s.mysqlInitialQueryPreview, "初始查询", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, s.initialSQLLoadFailed).Changed() {
				s.mysqlInitialQuery = s.mysqlInitialQueryPreview
			}
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "编辑初始查询", s.initialSQLLoadFailed, s.beginMySQLInitialQuery)
		})
		if ui.Button(c, locales.Text("兼容性")).Width(160).Height(theme.RowHeight(c, connectionFormInputHeight)).Clicked() {
			s.mysqlCompatibilityVisible = true
			s.connectionEditorTab = 6
		}
		note := "高级及兼容性选项可保存；初始查询仅本次编辑有效，不保存或执行。"
		if a.LimitSessions {
			note = "会话限制仅作用于本连接的 SQL 标签（安全上限 8）；元数据连接独立。"
		}
		ui.Text(c, note).TextColor(palette.Muted).FontSize(theme.Font(c, 11))
	})
}

func (s *State) mysqlAdvancedToggle(c *ui.Context, v *bool, label string) {
	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, v, label, false)
	})
}

func (s *State) mysqlAdvancedNumberRow(
	c *ui.Context, enabled *bool, label string, value *string, labelWidth float32,
) {
	connectionFormRow(c).Children(func() {
		connectionFormCheckboxCompact(c, enabled, label, false).Width(labelWidth)
		connectionFormTextInput(c, value, label+"数值", 80, !*enabled, false)
	})
}

// The original inserts this tab only after pressing the Compatibility button.
// Each override is enabled by both the parent switch and its own checkbox.
func (s *State) connectionMySQLCompatibility(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	formWidth := connectionFormWidth(width)
	compat := &s.connectionDraft.MySQLAdvanced.Compatibility
	labelWidth := float32(216)
	ui.Column(c).Width(formWidth).Gap(5).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 26)).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, &compat.Enabled, "使用兼容模式", true)
		})
		s.compatSelectRow(c, "强制 lower_case_table_names 为：",
			&compat.ForceLowerCase, &compat.LowerCaseTableNames,
			connections.MySQLLowerCaseValues, false, labelWidth)
		ui.Row(c).Height(15).Children(func() {
			ui.Box(c).Width(17)
			ui.Text(c, "跳过 \"show variables like 'lower_case_%'\"").FontSize(theme.Font(c, 11))
		})
		ui.Row(c).Height(theme.RowHeight(c, 26)).Gap(0).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, &compat.ForceSQLMode, "强制 sql_mode 为：", true).Width(labelWidth)
			ui.TextInput(c, &compat.SQLMode).Label("sql_mode").
				Width(258).Height(theme.RowHeight(c, 25)).Padding(2, 8).
				Disabled(true)
		})
		s.compatSelectRow(c, "强制 NDB 集群支持为：",
			&compat.ForceNDB, &compat.NDBSupport,
			connections.MySQLNDBSupportValues, false, labelWidth)
		s.compatSelectRow(c, "强制数据库列出方法为：",
			&compat.ForceDatabaseMethod, &compat.DatabaseMethod,
			connections.MySQLDatabaseMethods, false, labelWidth)
		s.compatSelectRow(c, "强制视图列出方法为：",
			&compat.ForceViewMethod, &compat.ViewMethod,
			connections.MySQLViewMethods, false, labelWidth)
		ui.Text(c, "当前版本不支持兼容性覆盖；这些选项保持禁用，旧配置保留。").
			TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		if ui.Button(c, "清除旧兼容性覆盖").Disabled(!compat.Enabled && !compat.ForceLowerCase && !compat.ForceSQLMode && !compat.ForceNDB && !compat.ForceDatabaseMethod && !compat.ForceViewMethod).Clicked() {
			compat.Enabled = false
			compat.ForceLowerCase = false
			compat.ForceSQLMode = false
			compat.ForceNDB = false
			compat.ForceDatabaseMethod = false
			compat.ForceViewMethod = false
		}
	})
}

func (s *State) compatSelectRow(
	c *ui.Context, name string, checked *bool, value *string,
	options []string, parentEnabled bool, labelWidth float32,
) {
	ui.Row(c).Height(theme.RowHeight(c, 26)).Gap(0).AlignItems(ui.Center).Children(func() {
		connectionFormCheckboxCompact(c, checked, name, !parentEnabled).Width(labelWidth)
		ui.Select(c, value, options).Label(name + "选项").Width(258).Height(theme.RowHeight(c, 25)).
			Disabled(!parentEnabled || !*checked)
	})
}

// connectionMySQLDatabases reproduces the offline Database tab: a custom-list
// toggle, a native two-column table and add/remove actions. The original reads
// server metadata when custom lists are enabled. NyaDB deliberately does not
// invent metadata or attempt a connection before a database driver exists.
func (s *State) connectionMySQLDatabases(c *ui.Context, width, height float32) {
	palette := theme.Colors(c)

	pageWidth := max(float32(520), width-40)
	pageHeight := max(float32(280), height-174)
	enabled := &s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList
	ui.Column(c).Width(pageWidth).Height(pageHeight).Gap(6).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, connectionFormInputHeight)).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, enabled, "使用自定义数据库列表", false)
		})
		columns := []ui.TableColumn{
			{Title: "数据库", Width: pageWidth - 92},
			{Title: "自动打开", Width: 88},
		}
		names := strings.FieldsFunc(s.connectionDraft.MySQLAdvanced.CustomDatabases, func(r rune) bool { return r == '\n' })
		theme.Table(c, &s.mysqlDatabaseTable, columns, len(names), func(row, col int) {
			if col == 0 {
				ui.Text(c, names[row])
			} else {
				ui.Text(c, "显式打开")
			}
		}).Width(pageWidth).Grow(1).Disabled(!*enabled)
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c, &s.customDatabaseName).Label("自定义数据库名称").Disabled(!*enabled).Grow(1)
			if ui.Button(c, "添加数据库到列表").Disabled(!*enabled || strings.TrimSpace(s.customDatabaseName) == "").Clicked() {
				name := strings.TrimSpace(s.customDatabaseName)
				found := false
				for _, n := range names {
					if n == name {
						found = true
					}
				}
				if !found && !strings.ContainsAny(name, "\n\r\x00") {
					names = append(names, name)
					s.connectionDraft.MySQLAdvanced.CustomDatabases = strings.Join(names, "\n")
				}
				s.customDatabaseName = ""
			}
			if ui.Button(c, "从列表中移除数据库").Disabled(!*enabled || len(names) == 0).Clicked() {
				names = names[:len(names)-1]
				s.connectionDraft.MySQLAdvanced.CustomDatabases = strings.Join(names, "\n")
			}
		})
		ui.Text(c, "列表只控制显示范围；不会创建数据库或提升访问权限。自动打开需要显式连接。").FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
	})
}

// SQL can contain literal credentials. It is never stored in Profile,
// sqlite configuration, logs or error reports. This editor is UI-only until
// encrypted storage and actual connection execution are independently built.
func (s *State) beginMySQLInitialQuery() {
	if !s.active || s.connectionDraft.Kind != connections.MySQL || s.initialSQLLoadFailed {
		return
	}
	s.mysqlInitialQueryDraft = s.mysqlInitialQuery
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = true
}

func (s *State) confirmMySQLInitialQuery() {
	if !s.mysqlInitialQueryOpen {
		return
	}
	if len(s.mysqlInitialQueryDraft) > 64*1024 || strings.ContainsRune(s.mysqlInitialQueryDraft, 0) {
		s.mysqlInitialQueryError = "初始查询不能超过 64 KiB，也不能包含 NUL 字符"
		return
	}
	s.mysqlInitialQuery = s.mysqlInitialQueryDraft
	s.mysqlInitialQueryPreview = strings.Join(strings.Fields(s.mysqlInitialQuery), " ")
	s.mysqlInitialQueryDraft = ""
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = false
}

func (s *State) cancelMySQLInitialQuery() {
	s.mysqlInitialQueryDraft = ""
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = false
}

func (s *State) clearMySQLInitialQuery() {
	s.mysqlInitialQuery = ""
	s.mysqlInitialQueryPreview = ""
	s.cancelMySQLInitialQuery()
}

func (s *State) mysqlInitialQueryModal(c *ui.Context) {
	palette := theme.Colors(c)

	if !s.mysqlInitialQueryOpen {
		// Escape and backdrop dismissal can close a native MyGo modal
		// without passing through the Cancel button. Discard its temporary
		// text on the next frame as well.
		if s.mysqlInitialQueryDraft != "" || s.mysqlInitialQueryError != "" {
			s.cancelMySQLInitialQuery()
		}
		return
	}
	width, height := c.Size()
	dialogWidth := min(float32(700), width-40)
	dialogHeight := min(float32(500), height-40)
	ui.Modal(c, &s.mysqlInitialQueryOpen, func() {
		ui.Column(c).Width(dialogWidth).Height(dialogHeight).Padding(15, 24).
			Background(palette.Header).Gap(14).Children(func() {
			ui.Row(c).Justify(ui.Center).Children(func() {
				ui.Text(c, "初始查询").Bold().FontSize(theme.Font(c, 13))
			})
			ui.Text(c, "初始查询：").FontSize(theme.Font(c, 12))
			ui.TextArea(c, &s.mysqlInitialQueryDraft).
				Label("初始查询 SQL 编辑器").
				Placeholder("e.g.\nSET ROLE ALL;\nSET @var_name = 'value';").
				Width(min(float32(602), dialogWidth-48)).Grow(1).MinHeight(120).FontSize(theme.Font(c, 12))
			ui.Row(c).Children(func() {
				if ui.Button(c, "复制到剪贴板").Clicked() {
					c.WriteClipboard(s.mysqlInitialQueryDraft)
				}
			})
			if s.mysqlInitialQueryError != "" {
				ui.Text(c, s.mysqlInitialQueryError).
					FontSize(theme.Font(c, 12)).TextColor(palette.Error)
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "确定").Label("确认初始查询").Width(80).Clicked() {
					s.confirmMySQLInitialQuery()
				}
				if ui.Button(c, "取消").Label("取消初始查询").Width(80).Clicked() {
					s.cancelMySQLInitialQuery()
				}
			})
		})
	})
}
