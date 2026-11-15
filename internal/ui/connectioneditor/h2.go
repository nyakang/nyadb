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

var h2Modes = []struct{ value, caption string }{{"file", "本地文件"}, {"memory", "内存数据库"}, {"tcp", "TCP 服务器"}, {"ssl", "TLS 服务器"}}
var h2Profiles = []struct{ value, caption string }{{"h2-v1", "H2 1.4"}, {"h2-v2", "H2 2.0 / 2.1"}, {"h2-v3", "H2 2.2 / 2.3 / 2.4"}, {"h2-legacy", "H2 Legacy"}}
var h2NumberLabels = []string{"连接超时（秒）", "查询超时（秒）", "连接池等待（毫秒）", "最大连接数", "元数据预留连接数", "SQL 会话上限"}

// h2FileBasePath keeps the selected or typed file name separate from the
// JDBC database base path; both current and legacy file suffixes are accepted.
func h2FileBasePath(path string) string {
	return connections.H2FileBasePath(path)
}

func (s *State) initializeH2Inputs() {
	a := s.connectionDraft.H2Options
	for _, mode := range h2Modes {
		if mode.value == a.Mode {
			s.h2ModeChoice = mode.caption
		}
	}
	for _, profile := range h2Profiles {
		if profile.value == a.DriverProfile {
			s.h2ProfileChoice = profile.caption
		}
	}
	for i, n := range []int{a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.PoolWaitTimeoutMilliseconds, a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections} {
		s.h2Numbers[i] = strconv.Itoa(n)
	}
	s.h2DriverChoice = a.DriverVersion
}

func (s *State) setH2Mode(mode string) {
	p := &s.connectionDraft
	if p.Kind != connections.H2 || p.H2Options.Mode == mode {
		return
	}
	valid := false
	for _, item := range h2Modes {
		valid = valid || item.value == mode
	}
	if !valid {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	oldNetwork := p.H2Options.Mode == "tcp" || p.H2Options.Mode == "ssl"
	p.H2Options.Mode = mode
	p.H2Options.ReadOnly = false
	p.H2Options.AutoServer = mode == "file"
	if mode == "memory" {
		p.Host, p.Port, s.connectionPort, p.Database = "", 0, "", "nyadb"
		p.H2Options.RequireExisting = false
	} else if mode == "file" {
		p.Host, p.Port, s.connectionPort, p.Database = "", 0, "", ""
		p.H2Options.RequireExisting = true
	} else if !oldNetwork {
		p.Host, p.Port, s.connectionPort, p.Database = "localhost", 9092, "9092", ""
		p.H2Options.RequireExisting = true
	}
	for _, item := range h2Modes {
		if item.value == mode {
			s.h2ModeChoice = item.caption
		}
	}
	s.connectionError = ""
}

func (s *State) editorH2Fields(c *ui.Context, labelWidth, fieldWidth float32) {
	p, a := &s.connectionDraft, &s.connectionDraft.H2Options
	var modes, profiles []string
	for _, item := range h2Modes {
		modes = append(modes, item.caption)
	}
	for _, item := range h2Profiles {
		profiles = append(profiles, item.caption)
	}
	theme.Field(c, locales.Text("连接方式："), func() { connectionFormSelect(c, &s.h2ModeChoice, modes, "H2 连接方式", fieldWidth, false) })
	for _, item := range h2Modes {
		if item.caption == s.h2ModeChoice {
			s.setH2Mode(item.value)
		}
	}
	theme.Field(c, locales.Text("H2 版本："), func() { connectionFormSelect(c, &s.h2ProfileChoice, profiles, "H2 版本", fieldWidth, false) })
	for _, item := range h2Profiles {
		if item.caption == s.h2ProfileChoice && item.value != a.DriverProfile {
			s.cancelMySQLConnectionTest()
			s.engineFileVersion++
			a.DriverProfile = item.value
			s.RefreshDriverPackages()
		}
	}
	s.h2DriverField(c, fieldWidth)
	if a.Mode == "tcp" || a.Mode == "ssl" {
		s.editorInputRow(c, "主机：", "主机", &p.Host, labelWidth, fieldWidth)
		s.editorInputRow(c, "端口：", "端口", &s.connectionPort, labelWidth, 100)
	}
	theme.Field(c, locales.Text("数据库："), func() {
		if a.Mode == "file" {
			ui.Row(c).Gap(0).FillWidth().AlignItems(ui.Center).Children(func() {
				s.formTextInput(c, &p.Database, "H2 数据库", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
				ui.Box(c).Width(connectionFormBrowseGap)
				connectionFormBrowse(c, "浏览文件", s.engineFilePicker == nil, func() {
					kind := H2MainFile
					if !a.RequireExisting {
						kind = H2NewFile
					}
					s.engineFilePicker(kind, -1, s.engineFileVersion)
				})
			})
		} else {
			s.formTextInput(c, &p.Database, "H2 数据库", fieldWidth, false, false)
		}
	})
	if a.Mode != "memory" {
		before := a.RequireExisting
		connectionFormCheckbox(c, &a.RequireExisting, "仅打开现有数据库", a.ReadOnly)
		if before != a.RequireExisting {
			s.engineFileVersion++
			s.cancelMySQLConnectionTest()
		}
	} else {
		ui.Text(c, locales.Text("内存数据库在断开连接后释放；恢复连接不会重放 SQL。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	}
	s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "密码", fieldWidth, false, true) })
	connectionFormCheckbox(c, &s.connectionSavePassword, "保存密码", s.credentialVault == nil)
}

func (s *State) h2DriverField(c *ui.Context, fieldWidth float32) {
	choices := []string{"未选择驱动包"}
	for _, d := range s.driverWork.packages {
		choices = append(choices, d.Version)
	}
	if s.h2DriverChoice == "" {
		s.h2DriverChoice = choices[0]
	}
	old := s.h2DriverChoice
	theme.Field(c, locales.Text("驱动包："), func() {
		connectionFormSelect(c, &s.h2DriverChoice, choices, "H2 驱动包", fieldWidth, s.driverWork.results != nil)
	})
	if old != s.h2DriverChoice {
		if s.h2DriverChoice == choices[0] {
			s.cancelMySQLConnectionTest()
			s.connectionDraft.H2Options.DriverVersion, s.connectionDraft.H2Options.DriverSHA256 = "", ""
		} else {
			for _, d := range s.driverWork.packages {
				if d.Version == s.h2DriverChoice {
					s.selectH2Driver(d)
					break
				}
			}
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, locales.Text("刷新驱动包")).Disabled(s.driverPackages.List == nil || s.driverWork.results != nil).Clicked() {
			s.RefreshDriverPackages()
		}
		if ui.Button(c, locales.Text("导入驱动包")).Disabled(s.engineFilePicker == nil || s.driverPackages.Import == nil || s.driverWork.results != nil).Clicked() {
			s.engineFilePicker(H2DriverPackageFile, -1, s.engineFileVersion)
		}
	})
	if s.driverWork.message != "" {
		ui.Text(c, locales.Text(s.driverWork.message)).Width(fieldWidth).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Error)
	}
}

func (s *State) h2Advanced(c *ui.Context, width float32) {
	a := &s.connectionDraft.H2Options
	formWidth := connectionScaledFormWidth(c, width)
	theme.Form(c, formWidth, func() {
		before := a.ReadOnly
		connectionFormCheckbox(c, &a.ReadOnly, "只读连接", a.Mode != "file")
		if before != a.ReadOnly {
			s.cancelMySQLConnectionTest()
			if a.ReadOnly {
				a.RequireExisting = true
				a.AutoServer = false
			}
		}
		beforeShared := a.AutoServer
		connectionFormCheckbox(c, &a.AutoServer, "共享本地文件连接", a.Mode != "file" || a.ReadOnly)
		if beforeShared != a.AutoServer {
			s.cancelMySQLConnectionTest()
		}
		if a.Mode == "file" {
			ui.Text(c, locales.Text("共享模式允许多个连接打开同一文件；只读连接使用独占模式。")).Width(formWidth).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
		}
		s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, connectionFormLabelWidth, formWidth-connectionFormLabelWidth)
		for i, label := range h2NumberLabels {
			s.editorInputRow(c, label, label, &s.h2Numbers[i], connectionFormLabelWidth, 120)
		}
		ui.Text(c, locales.Text("SQL 与数据操作共享连接池；元数据预留连接用于浏览数据库结构。")).Width(formWidth).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	})
}

func (s *State) advanceH2Options(p *connections.Profile) error {
	a := &p.H2Options
	values := []*int{&a.ConnectTimeoutSeconds, &a.QueryTimeoutSeconds, &a.PoolWaitTimeoutMilliseconds, &a.MaxConnections, &a.MetadataReserve, &a.SQLMaxConnections}
	minima, maxima := []int{1, 0, 250, 1, 0, 1}, []int{300, 86400, 300000, 32, 31, 32}
	for i, raw := range s.h2Numbers {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < minima[i] || n > maxima[i] {
			return &FieldError{Field: h2NumberLabels[i], Page: "高级", Message: fmt.Sprintf(locales.Text("%s：请输入有效范围内的整数"), locales.Text(h2NumberLabels[i]))}
		}
		*values[i] = n
	}
	if a.MetadataReserve >= a.MaxConnections {
		return fieldError("元数据预留连接数", "高级", "元数据预留必须小于最大连接数")
	}
	if a.SQLMaxConnections > a.MaxConnections-a.MetadataReserve {
		return fieldError("SQL 会话上限", "高级", "SQL 会话上限不能超过扣除元数据预留后的容量")
	}
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return fieldError("H2 驱动包", "常规", "请选择或导入已安装的 H2 驱动包")
	}
	for _, d := range s.driverWork.packages {
		if d.Version == a.DriverVersion && d.SHA256 == a.DriverSHA256 && !d.Supports(a.DriverProfile) {
			return fieldError("H2 版本", "常规", "驱动包不包含当前 H2 版本，请选择匹配的版本。")
		}
	}
	if _, err := connections.NormalizeH2Options(*a); err != nil {
		return fieldError("最大连接数", "高级", "请检查 H2 超时、连接池、预留容量和 SQL 会话上限")
	}
	return nil
}
