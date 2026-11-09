package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func neo4jAuthCaption(auth string) string {
	if auth == "none" {
		return "无认证"
	}
	return "用户名和密码"
}
func neo4jProtocolCaption(protocol string) string {
	if protocol == "neo4j" {
		return "Neo4j 路由"
	}
	return "Bolt 直连"
}
func neo4jTLSCaption(mode string) string {
	switch mode {
	case "verify-full":
		return "验证服务器证书"
	case "require":
		return "接受自签名证书"
	default:
		return "禁用 TLS"
	}
}
func (s *State) setNeo4jAuth(auth string) {
	if s.connectionDraft.Kind != connections.Neo4j || auth == s.connectionDraft.Neo4jOptions.Auth || auth != "none" && auth != "basic" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.Neo4jOptions.Auth = auth
	s.neo4jAuthChoice = neo4jAuthCaption(auth)
	s.connectionSecret = ""
	s.connectionSavePassword = false
	if auth == "none" {
		s.connectionDraft.Username = ""
	} else if s.connectionDraft.Username == "" {
		s.connectionDraft.Username = "neo4j"
	}
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}
func (s *State) editorNeo4jFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	theme.Field(c, locales.Text("连接方式："), func() {
		connectionFormSelect(c, &s.neo4jProtocolChoice, []string{"Bolt 直连", "Neo4j 路由"}, "Neo4j 连接方式", fieldWidth, false)
	})
	protocol := "bolt"
	if s.neo4jProtocolChoice == "Neo4j 路由" {
		protocol = "neo4j"
	}
	if protocol != p.Neo4jOptions.Protocol {
		s.cancelMySQLConnectionTest()
		s.engineFileVersion++
		p.Neo4jOptions.Protocol = protocol
		if protocol == "bolt" {
			p.Neo4jOptions.RoutingContext = ""
		}
	}
	s.editorInputRow(c, "初始数据库：", "Neo4j 初始数据库", &p.Database, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.neo4jAuthChoice, []string{"用户名和密码", "无认证"}, "Neo4j 认证方式", fieldWidth, false)
	})
	if s.neo4jAuthChoice == "无认证" {
		s.setNeo4jAuth("none")
	} else {
		s.setNeo4jAuth("basic")
	}
	if p.Neo4jOptions.Auth == "basic" {
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
		theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "密码", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存密码", s.credentialVault == nil) })
	}
}
func (s *State) neo4jOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.Neo4jOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "连接超时（秒）", "Neo4j 连接超时", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "请求超时（秒）", "Neo4j 请求超时", &s.engineNumbers[1], labelWidth, 120)
			if o.Protocol == "neo4j" {
				s.editorInputRow(c, "路由参数：", "Neo4j 路由参数", &o.RoutingContext, labelWidth, fieldWidth)
			}
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.neo4jTLSChoice, []string{"禁用 TLS", "验证服务器证书", "接受自签名证书"}, "Neo4j TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		switch s.neo4jTLSChoice {
		case "验证服务器证书":
			mode = "verify-full"
		case "接受自签名证书":
			mode = "require"
		}
		if mode != s.connectionDraft.TLSMode {
			s.cancelMySQLConnectionTest()
			s.engineFileVersion++
			s.connectionDraft.TLSMode = mode
		}
		s.neo4jFileRow(c, "CA 证书", "Neo4j CA 证书", "选择 Neo4j CA 证书", &o.CAFile, Neo4jCAFile, fieldWidth)
		s.neo4jFileRow(c, "客户端证书", "Neo4j 客户端证书", "选择 Neo4j 客户端证书", &o.ClientCertFile, Neo4jCertificateFile, fieldWidth)
		s.neo4jFileRow(c, "客户端私钥", "Neo4j 客户端私钥", "选择 Neo4j 客户端私钥", &o.ClientKeyFile, Neo4jKeyFile, fieldWidth)
		ui.Text(c, locales.Text("证书名称须与主机匹配；接受自签名证书会跳过服务器身份验证。"))
	})
	return true
}

func (s *State) neo4jFileRow(c *ui.Context, caption, name, browse string, target *string, kind EngineFile, width float32) {
	theme.Field(c, locales.Text(caption), func() {
		ui.Row(c).Gap(connectionFormBrowseGap).Grow(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
			s.formTextInput(c, target, name, width-connectionFormBrowseSize-connectionFormBrowseGap, false, false)
			connectionFormBrowse(c, browse, s.engineFilePicker == nil, func() {
				if s.engineFilePicker != nil {
					s.engineFilePicker(kind, -1, s.engineFileVersion)
				}
			})
		})
	})
}
func (s *State) advanceNeo4jOptions(p *connections.Profile) error {
	values := []*int{&p.Neo4jOptions.ConnectTimeoutSeconds, &p.Neo4jOptions.RequestTimeoutSeconds}
	labels := []string{"Neo4j 连接超时", "Neo4j 请求超时"}
	for i, target := range values {
		n, e := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		limit := 120
		if i == 1 {
			limit = 600
		}
		if e != nil || n < 1 || n > limit {
			return fieldError(labels[i], "高级", "请检查 Neo4j 超时设置")
		}
		*target = n
	}
	return nil
}
func (s *State) validateNeo4jDraft(p connections.Profile) error {
	if strings.TrimSpace(p.Host) == "" {
		return fieldError("主机", "常规", "请输入数据库主机")
	}
	if p.Neo4jOptions.Auth == "basic" && p.Username == "" {
		return fieldError("用户名", "常规", "请输入 Neo4j 用户名")
	}
	if _, e := connections.NormalizeNeo4jOptions(p.Neo4jOptions); e != nil {
		return fieldError("Neo4j 路由参数", "高级", "请检查 Neo4j 连接方式、路由参数和证书设置")
	}
	c, e := connections.Neo4jProfileConfig(p)
	if e != nil {
		return fieldError("Neo4j TLS 模式", "SSL", "请检查 Neo4j 主机、数据库、认证和 TLS 设置")
	}
	if connections.ValidateNeo4jDraftCredential(c, s.connectionSecret) != nil {
		return fieldError("密码", "常规", "请检查 Neo4j 密码")
	}
	return nil
}
