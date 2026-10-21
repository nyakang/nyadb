package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
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
	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	a := &s.connectionDraft.MySQLAdvanced
	// Browse buttons and Select elements need a 32-DIP row to keep their
	// content inside the row; 26 DIP clips/overlaps the following controls.
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			ui.Text(c, "设置位置：").Width(labelWidth).FontSize(12)
			connectionFormTextInput(c, &a.SettingsLocation, "设置位置", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "选择设置位置", s.mysqlSettingsPicker == nil, s.mysqlSettingsPicker)
		})
		connectionFormRow(c).Children(func() {
			ui.Text(c, "客户端驱动程序：").Width(labelWidth).FontSize(12)
			connectionFormSelect(c, &a.ClientDriver, []string{"默认", "旧版"}, "客户端驱动程序", fieldWidth, false)
		})
		connectionFormRow(c).Children(func() {
			ui.Text(c, "客户端字符集：").Width(labelWidth).FontSize(12)
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
			ui.Text(c, "初始查询：").Width(labelWidth).FontSize(12)
			if connectionFormTextInput(c, &s.mysqlInitialQueryPreview, "初始查询", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false).Changed() {
				s.mysqlInitialQuery = s.mysqlInitialQueryPreview
			}
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "编辑初始查询", false, s.beginMySQLInitialQuery)
		})
		if ui.Button(c, "兼容性").Width(160).Height(connectionFormInputHeight).Clicked() {
			s.mysqlCompatibilityVisible = true
			s.connectionEditorTab = 6
		}
		ui.Text(c, "高级及兼容性选项可保存；初始查询仅本次编辑有效，不保存或执行。").
			TextColor(theme.Muted).FontSize(11)
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
	formWidth := min(float32(604), width-44)
	compat := &s.connectionDraft.MySQLAdvanced.Compatibility
	labelWidth := float32(216)
	ui.Column(c).Width(formWidth).Gap(5).Children(func() {
		ui.Row(c).Height(26).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, &compat.Enabled, "使用兼容模式", false)
		})
		s.compatSelectRow(c, "强制 lower_case_table_names 为：",
			&compat.ForceLowerCase, &compat.LowerCaseTableNames,
			connections.MySQLLowerCaseValues, compat.Enabled, labelWidth)
		ui.Row(c).Height(15).Children(func() {
			ui.Box(c).Width(17)
			ui.Text(c, "跳过 \"show variables like 'lower_case_%'\"").FontSize(11)
		})
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, &compat.ForceSQLMode, "强制 sql_mode 为：", !compat.Enabled).Width(labelWidth)
			ui.TextInput(c, &compat.SQLMode).Label("sql_mode").
				Width(258).Height(25).Padding(2, 8).
				Disabled(!compat.Enabled || !compat.ForceSQLMode)
		})
		s.compatSelectRow(c, "强制 NDB 集群支持为：",
			&compat.ForceNDB, &compat.NDBSupport,
			connections.MySQLNDBSupportValues, compat.Enabled, labelWidth)
		s.compatSelectRow(c, "强制数据库列出方法为：",
			&compat.ForceDatabaseMethod, &compat.DatabaseMethod,
			connections.MySQLDatabaseMethods, compat.Enabled, labelWidth)
		s.compatSelectRow(c, "强制视图列出方法为：",
			&compat.ForceViewMethod, &compat.ViewMethod,
			connections.MySQLViewMethods, compat.Enabled, labelWidth)
		ui.Text(c, "兼容性设置仅保存为配置，数据库驱动接入后才会实际应用。").
			TextColor(theme.Muted).FontSize(11)
	})
}

func (s *State) compatSelectRow(
	c *ui.Context, name string, checked *bool, value *string,
	options []string, parentEnabled bool, labelWidth float32,
) {
	ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
		connectionFormCheckboxCompact(c, checked, name, !parentEnabled).Width(labelWidth)
		ui.Select(c, value, options).Label(name + "选项").Width(258).Height(25).
			Disabled(!parentEnabled || !*checked)
	})
}
