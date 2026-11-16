package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var zookeeperNumberLabels = []string{"ZooKeeper 会话超时（毫秒）", "ZooKeeper 连接超时（毫秒）", "ZooKeeper 缓冲区（字节）", "ZooKeeper 文档连接上限"}

func zookeeperAuthCaption(auth string) string {
	switch auth {
	case "digest":
		return "Digest"
	case "sasl_digest":
		return "SASL Digest"
	default:
		return "无认证"
	}
}

func zookeeperTLSCaption(mode string) string {
	if mode == "verify-full" {
		return "验证服务器证书"
	}
	return "禁用 TLS"
}

func (s *State) setZooKeeperAuth(auth string) {
	if s.connectionDraft.Kind != connections.ZooKeeper || s.connectionDraft.ZooKeeperOptions.Auth == auth || auth != "none" && auth != "digest" && auth != "sasl_digest" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.zookeeperAuthChoice, s.connectionDraft.ZooKeeperOptions.Auth = zookeeperAuthCaption(auth), auth
	s.connectionDraft.Username, s.connectionSecret = "", ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) setZooKeeperTLS(mode string) {
	if s.connectionDraft.Kind != connections.ZooKeeper || s.connectionDraft.TLSMode == mode || mode != "disable" && mode != "verify-full" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.zookeeperTLSChoice, s.connectionDraft.TLSMode = zookeeperTLSCaption(mode), mode
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) initializeZooKeeperInputs() {
	a := s.connectionDraft.ZooKeeperOptions
	for i, n := range []int{a.SessionTimeoutMS, a.ConnectionTimeoutMS, a.MaxBufferSize, a.MaxDocuments} {
		s.zookeeperNumbers[i] = strconv.Itoa(n)
	}
	s.zookeeperAuthChoice = zookeeperAuthCaption(a.Auth)
	s.zookeeperTLSChoice = zookeeperTLSCaption(s.connectionDraft.TLSMode)
	s.zookeeperDriverChoice = a.DriverVersion
}

func (s *State) editorZooKeeperFields(c *ui.Context, labelWidth, fieldWidth float32) {
	a := &s.connectionDraft.ZooKeeperOptions
	s.editorInputRow(c, "Chroot：", "ZooKeeper Chroot", &a.Chroot, labelWidth, fieldWidth)
	s.editorInputRow(c, "Namespace", "ZooKeeper Namespace", &a.Namespace, labelWidth, fieldWidth)
	s.editorInputRow(c, "键前缀：", "ZooKeeper 键前缀", &a.Prefix, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.zookeeperAuthChoice, []string{"无认证", "Digest", "SASL Digest"}, "ZooKeeper 认证方式", fieldWidth, false)
	})
	auth := "none"
	if s.zookeeperAuthChoice == "Digest" {
		auth = "digest"
	} else if s.zookeeperAuthChoice == "SASL Digest" {
		auth = "sasl_digest"
	}
	s.setZooKeeperAuth(auth)
	if a.Auth != "none" {
		s.editorInputRow(c, "用户名：", "用户名", &s.connectionDraft.Username, labelWidth, fieldWidth)
		theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "连接密码", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
	}
	versions := []string{"未选择驱动包"}
	for _, d := range s.driverWork.packages {
		versions = append(versions, d.Version)
	}
	if s.zookeeperDriverChoice == "" {
		s.zookeeperDriverChoice = versions[0]
	}
	old := s.zookeeperDriverChoice
	theme.Field(c, locales.Text("驱动包："), func() {
		connectionFormSelect(c, &s.zookeeperDriverChoice, versions, "ZooKeeper 驱动包", fieldWidth, s.DriverBusy())
	})
	if old != s.zookeeperDriverChoice {
		if s.zookeeperDriverChoice == versions[0] {
			s.cancelMySQLConnectionTest()
			a.DriverVersion, a.DriverSHA256 = "", ""
		} else {
			for _, d := range s.driverWork.packages {
				if d.Version == s.zookeeperDriverChoice {
					s.selectZooKeeperDriver(d)
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
			s.engineFilePicker(ZooKeeperDriverPackageFile, -1, s.engineFileVersion)
		}
	})
	if s.driverWork.message != "" {
		ui.Text(c, locales.Text(s.driverWork.message)).Width(fieldWidth).TextColor(theme.Colors(c).Error)
	}
}

func (s *State) selectZooKeeperDriver(d connections.ManagedDriver) {
	if d.Key != "zookeeper" || !d.Supports("zookeeper-v1") || d.JDBCClass != "" {
		s.driverWork.message = "驱动包不包含 ZooKeeper 原生驱动。"
		return
	}
	a := s.connectionDraft.ZooKeeperOptions
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = d.Version, d.SHA256, d.Directory
	if _, err := connections.NormalizeZooKeeperOptions(a); err != nil {
		s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.ZooKeeperOptions = a
	s.zookeeperDriverChoice = d.Version
	s.driverWork.message = ""
}

func (s *State) zookeeperOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	fieldWidth := formWidth - connectionFormLabelWidth
	a := &s.connectionDraft.ZooKeeperOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "其他服务器：", "ZooKeeper 其他服务器", &a.AdditionalServers, connectionFormLabelWidth, fieldWidth)
			labels := []string{"会话超时（毫秒）", "连接超时（毫秒）", "缓冲区（字节）", "文档连接上限"}
			for i, label := range zookeeperNumberLabels {
				s.editorInputRow(c, labels[i], label, &s.zookeeperNumbers[i], connectionFormLabelWidth, 120)
			}
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.zookeeperTLSChoice, []string{"禁用 TLS", "验证服务器证书"}, "ZooKeeper TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		if s.zookeeperTLSChoice == "验证服务器证书" {
			mode = "verify-full"
		}
		s.setZooKeeperTLS(mode)
		s.neo4jFileRow(c, "CA 证书", "ZooKeeper CA 证书", "选择 ZooKeeper CA 证书", &a.CAFile, ZooKeeperCAFile, fieldWidth)
		s.neo4jFileRow(c, "客户端证书", "ZooKeeper 客户端证书", "选择 ZooKeeper 客户端证书", &a.ClientCertFile, ZooKeeperCertificateFile, fieldWidth)
		s.neo4jFileRow(c, "客户端私钥", "ZooKeeper 客户端私钥", "选择 ZooKeeper 客户端私钥", &a.ClientKeyFile, ZooKeeperKeyFile, fieldWidth)
	})
	return true
}

func (s *State) advanceZooKeeperOptions(p *connections.Profile) error {
	a := &p.ZooKeeperOptions
	values := []*int{&a.SessionTimeoutMS, &a.ConnectionTimeoutMS, &a.MaxBufferSize, &a.MaxDocuments}
	minima, maxima := []int{1000, 250, 1024, 1}, []int{3600000, 300000, 256 << 20, 32}
	for i, raw := range s.zookeeperNumbers {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < minima[i] || n > maxima[i] {
			return fieldError(zookeeperNumberLabels[i], "高级", "请检查 ZooKeeper 超时、缓冲区和文档上限")
		}
		*values[i] = n
	}
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return fieldError("ZooKeeper 驱动包", "常规", "请选择或导入已安装的 ZooKeeper 驱动包")
	}
	return nil
}

func (s *State) validateZooKeeperDraft(p connections.Profile) error {
	if _, err := connections.ZooKeeperProfileConfig(p); err != nil {
		return fieldError("ZooKeeper Chroot", "常规", "请检查 ZooKeeper 服务器、命名空间和 TLS 设置")
	}
	if connections.ValidateZooKeeperCredential(p, s.connectionSecret) != nil {
		return fieldError("连接密码", "常规", "请检查 ZooKeeper 凭据")
	}
	return nil
}
