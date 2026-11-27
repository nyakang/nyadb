package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var databendNumberLabels = []string{"Databend 连接超时（秒）", "Databend 请求超时（秒）", "Databend 查询超时（秒）", "Databend 连接上限", "Databend 元数据保留连接", "Databend SQL 连接上限", "Databend 连接等待（毫秒）"}

func databendAuthCaption(auth string) string {
	switch auth {
	case "basic":
		return "用户名和密码"
	case "bearer":
		return "Bearer Token"
	default:
		return "无认证"
	}
}

func (s *State) setDatabendAuth(auth string) {
	if s.connectionDraft.Kind != connections.Databend || s.connectionDraft.DatabendOptions.Auth == auth || auth != "basic" && auth != "bearer" && auth != "none" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.databendAuthChoice, s.connectionDraft.DatabendOptions.Auth = databendAuthCaption(auth), auth
	s.connectionDraft.Username, s.connectionSecret = "", ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) setDatabendTLS(mode string) {
	if s.connectionDraft.Kind != connections.Databend || s.connectionDraft.TLSMode == mode || mode != "disable" && mode != "verify-full" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.connectionDraft.TLSMode = mode
	s.databendTLSChoice = "禁用 TLS"
	if mode == "verify-full" {
		s.databendTLSChoice = "验证服务器证书"
	}
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) initializeDatabendInputs() {
	a := s.connectionDraft.DatabendOptions
	for i, n := range []int{a.ConnectTimeoutSeconds, a.RequestTimeoutSeconds, a.QueryTimeoutSeconds, a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections, a.PoolWaitMillis} {
		s.databendNumbers[i] = strconv.Itoa(n)
	}
	s.databendAuthChoice, s.databendDriverChoice = databendAuthCaption(a.Auth), a.DriverVersion
	s.databendTLSChoice = "禁用 TLS"
	if s.connectionDraft.TLSMode == "verify-full" {
		s.databendTLSChoice = "验证服务器证书"
	}
}

func (s *State) editorDatabendFields(c *ui.Context, labelWidth, fieldWidth float32) {
	a := &s.connectionDraft.DatabendOptions
	s.editorInputRow(c, "数据库：", "初始数据库", &s.connectionDraft.Database, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.databendAuthChoice, []string{"用户名和密码", "Bearer Token", "无认证"}, "Databend 认证方式", fieldWidth, false)
	})
	auth := "none"
	if s.databendAuthChoice == "用户名和密码" {
		auth = "basic"
	} else if s.databendAuthChoice == "Bearer Token" {
		auth = "bearer"
	}
	s.setDatabendAuth(auth)
	if a.Auth == "basic" {
		s.editorInputRow(c, "用户名：", "用户名", &s.connectionDraft.Username, labelWidth, fieldWidth)
	}
	if a.Auth != "none" {
		caption, label := "密码：", "连接密码"
		if a.Auth == "bearer" {
			caption, label = "令牌：", "连接令牌"
		}
		theme.Field(c, locales.Text(caption), func() { s.formTextInput(c, &s.connectionSecret, label, fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
	}
	versions := []string{"未选择驱动包"}
	for _, d := range s.driverWork.packages {
		versions = append(versions, d.Version)
	}
	if s.databendDriverChoice == "" {
		s.databendDriverChoice = versions[0]
	}
	old := s.databendDriverChoice
	theme.Field(c, locales.Text("驱动包："), func() {
		connectionFormSelect(c, &s.databendDriverChoice, versions, "Databend 驱动包", fieldWidth, s.DriverBusy())
	})
	if old != s.databendDriverChoice {
		if s.databendDriverChoice == versions[0] {
			s.cancelMySQLConnectionTest()
			a.DriverVersion, a.DriverSHA256 = "", ""
		} else {
			for _, d := range s.driverWork.packages {
				if d.Version == s.databendDriverChoice {
					s.selectDatabendDriver(d)
					break
				}
			}
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, locales.Text("刷新驱动包")).Disabled(s.DriverBusy() || s.driverPackages.List == nil).Clicked() {
			s.RefreshDriverPackages()
		}
		if ui.Button(c, locales.Text("导入驱动包")).Disabled(s.DriverBusy() || s.driverPackages.Import == nil || s.engineFilePicker == nil).Clicked() {
			s.engineFilePicker(DatabendDriverPackageFile, -1, s.engineFileVersion)
		}
	})
	if s.driverWork.message != "" {
		ui.Text(c, locales.Text(s.driverWork.message)).Width(fieldWidth).TextColor(theme.Colors(c).Error)
	}
}

func (s *State) selectDatabendDriver(d connections.ManagedDriver) {
	if d.Key != "databend" || !d.Supports("databend") || d.JDBCClass != "" {
		s.driverWork.message = "驱动包不包含 Databend 驱动。"
		return
	}
	a := s.connectionDraft.DatabendOptions
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = d.Version, d.SHA256, d.Directory
	if _, err := connections.NormalizeDatabendOptions(a); err != nil {
		s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.DatabendOptions = a
	s.databendDriverChoice, s.driverWork.message = d.Version, ""
}

func (s *State) databendOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	fieldWidth := formWidth - connectionFormLabelWidth
	a := &s.connectionDraft.DatabendOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			for i, label := range databendNumberLabels {
				s.editorInputRow(c, strings.TrimPrefix(label, "Databend "), label, &s.databendNumbers[i], connectionFormLabelWidth, 120)
			}
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.databendTLSChoice, []string{"禁用 TLS", "验证服务器证书"}, "Databend TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		if s.databendTLSChoice == "验证服务器证书" {
			mode = "verify-full"
		}
		s.setDatabendTLS(mode)
		if mode == "disable" {
			return
		}
		s.editorInputRow(c, "服务器名称：", "Databend TLS 服务器名称", &a.ServerName, connectionFormLabelWidth, fieldWidth)
		s.neo4jFileRow(c, "CA 证书", "Databend CA 证书", "选择 Databend CA 证书", &a.CAFile, DatabendCAFile, fieldWidth)
		s.neo4jFileRow(c, "客户端证书", "Databend 客户端证书", "选择 Databend 客户端证书", &a.ClientCertFile, DatabendCertificateFile, fieldWidth)
		s.neo4jFileRow(c, "客户端私钥", "Databend 客户端私钥", "选择 Databend 客户端私钥", &a.ClientKeyFile, DatabendKeyFile, fieldWidth)
	})
	return true
}

func (s *State) advanceDatabendOptions(p *connections.Profile) error {
	a := &p.DatabendOptions
	values := []*int{&a.ConnectTimeoutSeconds, &a.RequestTimeoutSeconds, &a.QueryTimeoutSeconds, &a.MaxConnections, &a.MetadataReserve, &a.SQLMaxConnections, &a.PoolWaitMillis}
	minima, maxima := []int{1, 1, 0, 2, 1, 1, 250}, []int{3600, 3600, 86400, 32, 31, 31, 300000}
	for i, raw := range s.databendNumbers {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < minima[i] || n > maxima[i] {
			return fieldError(databendNumberLabels[i], "高级", "请检查 Databend 超时和连接上限")
		}
		*values[i] = n
	}
	if a.MetadataReserve >= a.MaxConnections || a.SQLMaxConnections > a.MaxConnections-a.MetadataReserve {
		return fieldError(databendNumberLabels[3], "高级", "请检查 Databend 超时和连接上限")
	}
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return fieldError("Databend 驱动包", "常规", "请选择或导入已安装的 Databend 驱动包")
	}
	if p.TLSMode == "disable" {
		a.CAFile, a.ServerName, a.ClientCertFile, a.ClientKeyFile = "", "", "", ""
	}
	return nil
}

func (s *State) validateDatabendDraft(p connections.Profile) error {
	if _, err := connections.DatabendProfileManagedConfig(p); err != nil {
		return fieldError("初始数据库", "常规", "请检查 Databend 服务器、数据库和 TLS 设置")
	}
	secret, auth := s.connectionSecret, p.DatabendOptions.Auth
	if strings.ContainsAny(secret, "\r\n\x00") || auth == "none" && secret != "" || auth == "bearer" && (secret == "" || strings.ContainsAny(secret, " \t")) {
		label := "连接密码"
		if auth == "bearer" {
			label = "连接令牌"
		}
		return fieldError(label, "常规", "请检查 Databend 凭据")
	}
	return nil
}
