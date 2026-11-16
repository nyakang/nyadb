package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var trinoModes = []struct{ value, caption string }{{"http", "HTTP / HTTPS"}, {"jdbc", "受管 JDBC"}}

func (s *State) initializeTrinoInputs() {
	a := s.connectionDraft.TrinoOptions
	for _, mode := range trinoModes {
		if mode.value == a.Mode {
			s.trinoModeChoice = mode.caption
		}
	}
	for i, n := range []int{a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.PoolWaitTimeoutMilliseconds, a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections} {
		s.trinoNumbers[i] = strconv.Itoa(n)
	}
	s.trinoDriverChoice = a.DriverVersion
}
func (s *State) setTrinoMode(mode string) {
	if s.connectionDraft.Kind != connections.Trino || mode == s.connectionDraft.TrinoOptions.Mode || mode != "http" && mode != "jdbc" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.cancelDriverWork()
	s.engineFileVersion++
	s.driverWork.packages = nil
	s.driverWork.message = ""
	s.connectionDraft.TrinoOptions.Mode = mode
	s.connectionEditorTab = 0
	for _, item := range trinoModes {
		if item.value == mode {
			s.trinoModeChoice = item.caption
		}
	}
	s.RefreshDriverPackages()
}
func (s *State) trinoDriverFields(c *ui.Context, width float32) {
	choices := []string{"HTTP / HTTPS", "受管 JDBC"}
	theme.Field(c, locales.Text("连接方式："), func() { connectionFormSelect(c, &s.trinoModeChoice, choices, "Trino 连接方式", width, false) })
	for _, item := range trinoModes {
		if item.caption == s.trinoModeChoice {
			s.setTrinoMode(item.value)
		}
	}
	if s.connectionDraft.TrinoOptions.Mode != "jdbc" {
		return
	}
	versions := []string{"未选择驱动包"}
	for _, d := range s.driverWork.packages {
		versions = append(versions, d.Version)
	}
	if s.trinoDriverChoice == "" {
		s.trinoDriverChoice = versions[0]
	}
	old := s.trinoDriverChoice
	theme.Field(c, locales.Text("驱动包："), func() {
		connectionFormSelect(c, &s.trinoDriverChoice, versions, "Trino 驱动包", width, s.DriverBusy())
	})
	if old != s.trinoDriverChoice {
		if s.trinoDriverChoice == versions[0] {
			s.cancelMySQLConnectionTest()
			s.connectionDraft.TrinoOptions.DriverVersion, s.connectionDraft.TrinoOptions.DriverSHA256 = "", ""
		} else {
			for _, d := range s.driverWork.packages {
				if d.Version == s.trinoDriverChoice {
					s.selectTrinoDriver(d)
					break
				}
			}
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, locales.Text("刷新驱动包")).Disabled(s.driverPackages.List == nil || s.DriverBusy()).Clicked() {
			s.RefreshDriverPackages()
		}
		if ui.Button(c, locales.Text("导入驱动包")).Disabled(s.engineFilePicker == nil || s.driverPackages.Import == nil || s.DriverBusy()).Clicked() {
			s.engineFilePicker(TrinoDriverPackageFile, -1, s.engineFileVersion)
		}
	})
	ui.Text(c, locales.Text("JDBC 的部分二进制、复合和时间值可能不完整；HTTP 模式保留原始类型。")).Width(width).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	if s.driverWork.message != "" {
		ui.Text(c, locales.Text(s.driverWork.message)).Width(width).TextColor(theme.Colors(c).Error)
	}
}
func (s *State) selectTrinoDriver(d connections.ManagedDriver) {
	if d.Key != "trino" || !d.Supports("trino") {
		s.driverWork.message = "驱动包不包含 Trino 驱动。"
		s.trinoDriverChoice = s.connectionDraft.TrinoOptions.DriverVersion
		return
	}
	a := s.connectionDraft.TrinoOptions
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = d.Version, d.SHA256, d.Directory
	if _, err := connections.NormalizeTrinoOptions(a); err != nil {
		s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.TrinoOptions = a
	s.trinoDriverChoice = d.Version
	s.driverWork.message = ""
}
func (s *State) trinoAdvanced(c *ui.Context, width float32) {
	if s.connectionDraft.TrinoOptions.Mode != "jdbc" {
		return
	}
	theme.Form(c, width, func() {
		for i, label := range h2NumberLabels {
			s.editorInputRow(c, label, label, &s.trinoNumbers[i], connectionFormLabelWidth, 120)
		}
		ui.Text(c, locales.Text("SQL 与数据操作共享连接池；元数据预留连接用于浏览数据库结构。")).Width(width).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	})
}
func (s *State) advanceTrinoOptions(p *connections.Profile) error {
	if p.TrinoOptions.Mode != "jdbc" {
		return nil
	}
	a := &p.TrinoOptions
	values := []*int{&a.ConnectTimeoutSeconds, &a.QueryTimeoutSeconds, &a.PoolWaitTimeoutMilliseconds, &a.MaxConnections, &a.MetadataReserve, &a.SQLMaxConnections}
	minima, maxima := []int{1, 0, 250, 1, 0, 1}, []int{300, 86400, 300000, 32, 31, 32}
	for i, raw := range s.trinoNumbers {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < minima[i] || n > maxima[i] {
			return fieldError(h2NumberLabels[i], "高级", "请检查 Trino 超时、连接池和 SQL 会话上限")
		}
		*values[i] = n
	}
	if _, err := connections.NormalizeTrinoOptions(*a); err != nil {
		return fieldError("最大连接数", "高级", "请检查 Trino 超时、连接池和 SQL 会话上限")
	}
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return fieldError("Trino 驱动包", "常规", "请选择或导入已安装的 Trino 驱动包")
	}
	return nil
}
