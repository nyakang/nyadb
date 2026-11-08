package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) advanceQdrantOptions(p *connections.Profile) error {
	values := []*int{&p.QdrantOptions.ConnectTimeoutSeconds, &p.QdrantOptions.RequestTimeoutSeconds}
	labels := []string{"Qdrant 连接超时", "Qdrant 请求超时"}
	for i, target := range values {
		n, err := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		limit := 120
		if i == 1 {
			limit = 600
		}
		if err != nil || n < 1 || n > limit {
			return fieldError(labels[i], "高级", "请检查 Qdrant 超时设置")
		}
		*target = n
	}
	return nil
}

func (s *State) setQdrantAuth(auth string) {
	if s.connectionDraft.Kind != connections.Qdrant || auth == s.connectionDraft.QdrantOptions.Auth || auth != "none" && auth != "api_key" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.QdrantOptions.Auth = auth
	s.qdrantAuthChoice = qdrantAuthCaption(auth)
	s.connectionDraft.Username = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func qdrantAuthCaption(auth string) string {
	if auth == "api_key" {
		return "API key"
	}
	return "无认证"
}

func (s *State) editorQdrantFields(c *ui.Context, labelWidth, fieldWidth float32) {
	o := &s.connectionDraft.QdrantOptions
	s.editorInputRow(c, "服务路径：", "Qdrant 服务路径", &o.ContextPath, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.qdrantAuthChoice, []string{"无认证", "API key"}, "Qdrant 认证方式", fieldWidth, false)
	})
	if s.qdrantAuthChoice == "API key" {
		s.setQdrantAuth("api_key")
	} else {
		s.setQdrantAuth("none")
	}
	if o.Auth == "api_key" {
		theme.Field(c, locales.Text("API key："), func() { s.formTextInput(c, &s.connectionSecret, "Qdrant API key", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
	}
}

func (s *State) qdrantOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.QdrantOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "连接超时（秒）", "Qdrant 连接超时", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "请求超时（秒）", "Qdrant 请求超时", &s.engineNumbers[1], labelWidth, 120)
			return
		}
		ui.Text(c, locales.Text("Qdrant REST 默认端口 6333，服务路径留空表示服务器根路径。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Qdrant TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Qdrant CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		s.editorInputRow(c, "客户端证书", "Qdrant 客户端证书", &o.ClientCertFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "客户端私钥", "Qdrant 客户端私钥", &o.ClientKeyFile, labelWidth, fieldWidth)
	})
	return true
}

func (s *State) validateQdrantDraft(p connections.Profile) error {
	if _, err := connections.NormalizeQdrantOptions(p.QdrantOptions); err != nil {
		return fieldError("Qdrant 服务路径", "常规", "请检查 Qdrant 服务路径和认证设置")
	}
	cfg, err := connections.QdrantProfileConfig(p)
	if err != nil {
		return fieldError("Qdrant TLS 模式", "SSL", "请检查 Qdrant TLS 和客户端证书设置")
	}
	if connections.ValidateQdrantCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("Qdrant API key", "常规", "请检查 Qdrant API key")
	}
	return nil
}
