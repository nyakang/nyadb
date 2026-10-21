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
	ui.Column(c).Width(formWidth).Gap(5).Children(func() {
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "设置位置：").Width(labelWidth).FontSize(12)
			ui.TextInput(c, &a.SettingsLocation).Label("设置位置").Width(fieldWidth-30).
				Height(25).Padding(2, 8)
			if ui.Button(c, "…").Label("选择设置位置").Width(28).
				Disabled(s.mysqlSettingsPicker == nil).Clicked() {
				s.mysqlSettingsPicker()
			}
		})
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "客户端驱动程序：").Width(labelWidth).FontSize(12)
			ui.Select(c, &a.ClientDriver, []string{"默认", "旧版"}).
				Label("客户端驱动程序").Width(fieldWidth).Height(26)
		})
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "客户端字符集：").Width(labelWidth).FontSize(12)
			ui.Select(c, &a.Charset, connections.MySQLCharsets).
				Label("客户端字符集").Width(fieldWidth).Height(26)
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
		ui.Row(c).Height(26).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "初始查询：").Width(labelWidth).FontSize(12)
			query := ""
			ui.TextInput(c, &query).Label("初始查询尚未支持").Width(fieldWidth-30).
				Height(25).Padding(2, 8).Disabled(true)
			ui.Button(c, "…").Label("编辑初始查询（尚未支持）").Width(28).Disabled(true)
		})
		if ui.Button(c, "兼容性").Width(160).Clicked() {
			s.mysqlCompatibilityVisible = true
			s.connectionEditorTab = 6
		}
		ui.Text(c, "高级选项可以保存为配置；真实连接与初始化 SQL 尚未接入。").
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

// The compatibility subtab exists in the original only after its button is
// used. Its driver-specific overrides are intentionally non-editable until
// their meaning and implementation have been independently specified.
func (s *State) connectionMySQLCompatibility(c *ui.Context, width float32) {
	formWidth := min(float32(604), width-44)
	ui.Column(c).Width(formWidth).Gap(14).Children(func() {
		enabled := false
		ui.Checkbox(c, &enabled, "使用兼容模式").Disabled(true)
		for _, name := range []string{
			"强制 lower_case_table_names 为：",
			"强制 sql_mode 为：",
			"强制 NDB 集群支持为：",
			"强制数据库列出方法为：",
			"强制视图列出方法为：",
		} {
			value := ""
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Checkbox(c, &enabled, name).Width(250).Disabled(true)
				ui.TextInput(c, &value).Width(220).Disabled(true)
			})
		}
		ui.Text(c, "兼容性高级覆盖需要真实 MySQL 驱动支持，目前不可修改或保存。").
			TextColor(theme.Muted).FontSize(11)
	})
}
