package connectioneditor

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func etcdAuthCaption(auth string) string {
	switch auth {
	case "basic":
		return "用户名和密码"
	case "certificate":
		return "客户端证书认证"
	default:
		return "无认证"
	}
}
func etcdPrefixCaption(encoding string) string {
	if encoding == "base64" {
		return "Base64"
	}
	return "文本"
}
func (s *State) setEtcdAuth(auth string) {
	o := &s.connectionDraft.EtcdOptions
	if s.connectionDraft.Kind != connections.Etcd || o.Auth == auth || auth != "none" && auth != "basic" && auth != "certificate" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	o.Auth = auth
	s.etcdAuthChoice = etcdAuthCaption(auth)
	s.connectionDraft.Username, s.connectionSecret = "", ""
	s.connectionSavePassword = false
	if auth == "none" {
		o.ClientCertFile, o.ClientKeyFile = "", ""
	}
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}
func (s *State) setEtcdPrefixEncoding(encoding string) {
	o := &s.connectionDraft.EtcdOptions
	if encoding == o.PrefixEncoding {
		return
	}
	bytes, err := connections.EtcdPrefixBytes(*o)
	candidate := *o
	candidate.PrefixEncoding = encoding
	if encoding == "base64" {
		candidate.Prefix = base64.StdEncoding.EncodeToString(bytes)
	} else {
		candidate.Prefix = string(bytes)
	}
	if _, e := connections.EtcdPrefixBytes(candidate); err != nil || e != nil {
		s.etcdPrefixChoice = etcdPrefixCaption(o.PrefixEncoding)
		s.validationError(fieldError("etcd 键前缀", "常规", "此键前缀不能使用所选编码，请检查文本或 Base64"))
		return
	}
	s.cancelMySQLConnectionTest()
	*o = candidate
	s.connectionError = ""
}
func (s *State) editorEtcdFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	theme.Field(c, locales.Text("前缀编码："), func() {
		connectionFormSelect(c, &s.etcdPrefixChoice, []string{"文本", "Base64"}, "etcd 前缀编码", fieldWidth, false)
	})
	encoding := "text"
	if s.etcdPrefixChoice == "Base64" {
		encoding = "base64"
	}
	s.setEtcdPrefixEncoding(encoding)
	s.editorInputRow(c, "键前缀：", "etcd 键前缀", &p.EtcdOptions.Prefix, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.etcdAuthChoice, []string{"无认证", "用户名和密码", "客户端证书认证"}, "etcd 认证方式", fieldWidth, false)
	})
	auth := "none"
	switch s.etcdAuthChoice {
	case "用户名和密码":
		auth = "basic"
	case "客户端证书认证":
		auth = "certificate"
	}
	s.setEtcdAuth(auth)
	if auth == "basic" {
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
		theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "密码", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存密码", s.credentialVault == nil) })
	}
	if auth == "certificate" {
		ui.Text(c, locales.Text("请在 SSL 页启用证书验证，并选择客户端证书和私钥。"))
	}
	ui.Text(c, locales.Text("空前缀表示全部键；前缀保留空格，二进制键请使用 Base64。"))
}
func (s *State) etcdOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.EtcdOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "连接超时（秒）", "etcd 连接超时", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "请求超时（秒）", "etcd 请求超时", &s.engineNumbers[1], labelWidth, 120)
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.etcdTLSChoice, []string{"禁用 TLS", "验证服务器证书"}, "etcd TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		if s.etcdTLSChoice == "验证服务器证书" {
			mode = "verify-full"
		}
		if mode != s.connectionDraft.TLSMode {
			s.cancelMySQLConnectionTest()
			s.engineFileVersion++
			s.connectionDraft.TLSMode = mode
		}
		s.neo4jFileRow(c, "CA 证书", "etcd CA 证书", "选择 etcd CA 证书", &o.CAFile, EtcdCAFile, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "etcd 证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		if o.Auth != "none" {
			s.neo4jFileRow(c, "客户端证书", "etcd 客户端证书", "选择 etcd 客户端证书", &o.ClientCertFile, EtcdCertificateFile, fieldWidth)
			s.neo4jFileRow(c, "客户端私钥", "etcd 客户端私钥", "选择 etcd 客户端私钥", &o.ClientKeyFile, EtcdKeyFile, fieldWidth)
		}
		ui.Text(c, locales.Text("TLS 验证证书链和服务器名称；客户端证书身份可能决定键权限。"))
	})
	return true
}
func (s *State) advanceEtcdOptions(p *connections.Profile) error {
	values := []*int{&p.EtcdOptions.ConnectTimeoutSeconds, &p.EtcdOptions.RequestTimeoutSeconds}
	labels := []string{"etcd 连接超时", "etcd 请求超时"}
	for i, target := range values {
		n, err := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		limit := 300
		if i == 1 {
			limit = 600
		}
		if err != nil || n < 1 || n > limit {
			return fieldError(labels[i], "高级", "请检查 etcd 超时设置")
		}
		*target = n
	}
	return nil
}
func (s *State) validateEtcdDraft(p connections.Profile) error {
	if _, err := connections.EtcdPrefixBytes(p.EtcdOptions); err != nil {
		return fieldError("etcd 键前缀", "常规", "此键前缀不能使用所选编码，请检查文本或 Base64")
	}
	if p.EtcdOptions.Auth == "basic" && p.Username == "" {
		return fieldError("用户名", "常规", "请输入 etcd 用户名")
	}
	cfg, err := connections.EtcdProfileConfig(p)
	if err != nil {
		return fieldError("etcd TLS 模式", "SSL", "请检查 etcd 主机、认证和 TLS 证书设置")
	}
	if connections.ValidateEtcdCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("密码", "常规", "请检查 etcd 密码")
	}
	return nil
}
