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
	formWidth := min(float32(604), width-44)
	labelWidth := min(float32(166), formWidth*0.3)
	fieldWidth := formWidth - labelWidth
	a := &s.connectionDraft.MySQLAdvanced
	// Browse buttons and Select elements need a 32-DIP row to keep their
	// content inside the row; 26 DIP clips/overlaps the following controls.
	ui.Column(c).Width(formWidth).Gap(5).Children(func() {
		ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "设置位置：").Width(labelWidth).FontSize(12)
			ui.TextInput(c, &a.SettingsLocation).Label("设置位置").Width(fieldWidth-34).
				Height(30).Padding(2, 8)
			ui.Box(c).Width(4)
			if ui.Button(c, "…").Label("选择设置位置").Width(30).Height(30).Padding(0, 0).
				Disabled(s.mysqlSettingsPicker == nil).Clicked() {
				s.mysqlSettingsPicker()
			}
		})
		ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "客户端驱动程序：").Width(labelWidth).FontSize(12)
			ui.Select(c, &a.ClientDriver, []string{"默认", "旧版"}).
				Label("客户端驱动程序").Width(fieldWidth).Height(30)
		})
		ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "客户端字符集：").Width(labelWidth).FontSize(12)
			ui.Select(c, &a.Charset, connections.MySQLCharsets).
				Label("客户端字符集").Width(fieldWidth).Height(30)
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
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &a.NamedPipe, "使用命名管道，套接字：").
				Label("使用命名管道，套接字").Width(labelWidth).FontSize(11)
			ui.TextInput(c, &a.PipeName).Label("命名管道或套接字").
				Width(200).Height(25).Padding(2, 8).Disabled(!a.NamedPipe)
		})
		s.mysqlAdvancedNumberRow(c, &a.LimitSessions, "限制连接会话：",
			&s.mysqlAdvancedNumbers[mysqlSessionLimit], labelWidth)
		ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "初始查询：").Width(labelWidth).FontSize(12)
			if ui.TextInput(c, &s.mysqlInitialQueryPreview).Label("初始查询").
				Width(fieldWidth-34).Height(30).Padding(2, 8).Changed() {
				s.mysqlInitialQuery = s.mysqlInitialQueryPreview
			}
			ui.Box(c).Width(4)
			if ui.Button(c, "…").Label("编辑初始查询").Width(30).Height(30).Padding(0, 0).Clicked() {
				s.beginMySQLInitialQuery()
			}
		})
		if ui.Button(c, "兼容性").Width(160).Clicked() {
			s.mysqlCompatibilityVisible = true
			s.connectionEditorTab = 6
		}
		ui.Text(c, "高级及兼容性选项可保存；初始查询仅本次编辑有效，不保存或执行。").
			TextColor(theme.Muted).FontSize(11)
	})
}

func (s *State) mysqlAdvancedToggle(c *ui.Context, v *bool, label string) {
	ui.Row(c).Height(26).AlignItems(ui.Center).Children(func() {
		ui.Checkbox(c, v, label).Label(label)
	})
}

func (s *State) mysqlAdvancedNumberRow(
	c *ui.Context, enabled *bool, label string, value *string, labelWidth float32,
) {
	ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
		ui.Checkbox(c, enabled, label).Label(label).Width(labelWidth)
		ui.TextInput(c, value).Label(label+"数值").Width(80).
			Height(25).Padding(2, 8).Disabled(!*enabled)
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
			ui.Checkbox(c, &compat.Enabled, "使用兼容模式").Label("使用兼容模式")
		})
		s.compatSelectRow(c, "强制 lower_case_table_names 为：",
			&compat.ForceLowerCase, &compat.LowerCaseTableNames,
			connections.MySQLLowerCaseValues, compat.Enabled, labelWidth)
		ui.Row(c).Height(15).Children(func() {
			ui.Box(c).Width(17)
			ui.Text(c, "跳过 \"show variables like 'lower_case_%'\"").FontSize(11)
		})
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &compat.ForceSQLMode, "强制 sql_mode 为：").
				Label("强制 sql_mode 为：").Width(labelWidth).
				Disabled(!compat.Enabled)
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
		ui.Checkbox(c, checked, name).Label(name).Width(labelWidth).
			FontSize(11).Disabled(!parentEnabled)
		ui.Select(c, value, options).Label(name + "选项").Width(258).Height(25).
			Disabled(!parentEnabled || !*checked)
	})
}
