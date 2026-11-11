package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func consulAuthCaption(auth string) string {
	if auth == "token" {
		return "Token"
	}
	return "无认证"
}
func consulConsistencyCaption(consistency string) string {
	switch consistency {
	case "consistent":
		return "一致读取"
	case "stale":
		return "允许过期读取"
	default:
		return "默认"
	}
}
func (s *State) setConsulAuth(auth string) {
	o := &s.connectionDraft.ConsulOptions
	if s.connectionDraft.Kind != connections.Consul || o.Auth == auth || auth != "none" && auth != "token" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	o.Auth = auth
	s.consulAuthChoice = consulAuthCaption(auth)
	s.connectionDraft.Username, s.connectionSecret = "", ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}
func (s *State) editorConsulFields(c *ui.Context, labelWidth, fieldWidth float32) {
	o := &s.connectionDraft.ConsulOptions
	s.editorInputRow(c, "数据中心：", "Consul 数据中心", &o.Datacenter, labelWidth, fieldWidth)
	s.editorInputRow(c, "键前缀：", "Consul 键前缀", &o.Prefix, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("读取一致性："), func() {
		connectionFormSelect(c, &s.consulConsistencyChoice, []string{"默认", "一致读取", "允许过期读取"}, "Consul 读取一致性", fieldWidth, false)
	})
	consistency := "default"
	if s.consulConsistencyChoice == "一致读取" {
		consistency = "consistent"
	} else if s.consulConsistencyChoice == "允许过期读取" {
		consistency = "stale"
	}
	if o.Consistency != consistency {
		s.cancelMySQLConnectionTest()
		o.Consistency = consistency
	}
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.consulAuthChoice, []string{"无认证", "Token"}, "Consul 认证方式", fieldWidth, false)
	})
	auth := "none"
	if s.consulAuthChoice == "Token" {
		auth = "token"
	}
	s.setConsulAuth(auth)
	if auth == "token" {
		theme.Field(c, locales.Text("Token："), func() { s.formTextInput(c, &s.connectionSecret, "Consul token", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
	}
	ui.Text(c, locales.Text("空数据中心使用服务默认值；键前缀保留原文本，仅限定浏览范围。"))
}
func (s *State) consulOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.ConsulOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "连接超时（秒）", "Consul 连接超时", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "请求超时（秒）", "Consul 请求超时", &s.engineNumbers[1], labelWidth, 120)
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.consulTLSChoice, []string{"禁用 TLS", "验证服务器证书"}, "Consul TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		if s.consulTLSChoice == "验证服务器证书" {
			mode = "verify-full"
		}
		if mode != s.connectionDraft.TLSMode {
			s.cancelMySQLConnectionTest()
			s.engineFileVersion++
			s.connectionDraft.TLSMode = mode
		}
		s.neo4jFileRow(c, "CA 证书", "Consul CA 证书", "选择 Consul CA 证书", &o.CAFile, ConsulCAFile, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "Consul 证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		s.neo4jFileRow(c, "客户端证书", "Consul 客户端证书", "选择 Consul 客户端证书", &o.ClientCertFile, ConsulCertificateFile, fieldWidth)
		s.neo4jFileRow(c, "客户端私钥", "Consul 客户端私钥", "选择 Consul 客户端私钥", &o.ClientKeyFile, ConsulKeyFile, fieldWidth)
		ui.Text(c, locales.Text("TLS 验证证书链和服务器名称；mTLS 与 Token 授权分别配置。"))
	})
	return true
}
func (s *State) advanceConsulOptions(p *connections.Profile) error {
	values := []*int{&p.ConsulOptions.ConnectTimeoutSeconds, &p.ConsulOptions.RequestTimeoutSeconds}
	labels := []string{"Consul 连接超时", "Consul 请求超时"}
	for i, target := range values {
		n, err := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		limit := 300
		if i == 1 {
			limit = 600
		}
		if err != nil || n < 1 || n > limit {
			return fieldError(labels[i], "高级", "请检查 Consul 超时设置")
		}
		*target = n
	}
	return nil
}
func (s *State) validateConsulDraft(p connections.Profile) error {
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		return fieldError("Consul TLS 模式", "SSL", "请检查 Consul 主机、数据中心、一致性和 TLS 设置")
	}
	if connections.ValidateConsulCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("Consul token", "常规", "请检查 Consul token")
	}
	return nil
}
