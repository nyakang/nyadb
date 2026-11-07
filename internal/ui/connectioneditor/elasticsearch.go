package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) setElasticsearchAuth(auth string) {
	if s.connectionDraft.Kind != connections.Elasticsearch || auth == s.connectionDraft.ElasticsearchOptions.Auth || auth != "none" && auth != "basic" && auth != "api_key" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.ElasticsearchOptions.Auth = auth
	s.elasticsearchAuthChoice = elasticsearchAuthCaption(auth)
	if auth != "basic" {
		s.connectionDraft.Username = ""
	}
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func elasticsearchAuthCaption(auth string) string {
	switch auth {
	case "none":
		return "无认证"
	case "api_key":
		return "API key"
	default:
		return "用户名和密码"
	}
}

func (s *State) editorElasticsearchFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.elasticsearchAuthChoice, []string{"用户名和密码", "API key", "无认证"}, "Elasticsearch 认证方式", fieldWidth, false)
	})
	switch s.elasticsearchAuthChoice {
	case "用户名和密码":
		s.setElasticsearchAuth("basic")
	case "API key":
		s.setElasticsearchAuth("api_key")
	case "无认证":
		s.setElasticsearchAuth("none")
	}
	if p.ElasticsearchOptions.Auth == "none" {
		return
	}
	if p.ElasticsearchOptions.Auth == "basic" {
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
	}
	caption, label := "密码：", "密码"
	if p.ElasticsearchOptions.Auth == "api_key" {
		caption, label = "API key：", "Elasticsearch API key"
	}
	theme.Field(c, locales.Text(caption), func() {
		s.formTextInput(c, &s.connectionSecret, label, fieldWidth, false, true)
	})
	ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() {
		connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil)
	})
}

func (s *State) elasticsearchSSL(c *ui.Context, width float32, tab string) bool {
	if tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.ElasticsearchOptions
	theme.Form(c, formWidth, func() {
		ui.Text(c, locales.Text("Elasticsearch 使用 REST 接口，默认端口 9200。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Elasticsearch TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Elasticsearch CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		ui.Text(c, locales.Text("密码和 API key 需要 HTTPS；HTTP 仅用于无认证连接。"))
	})
	return true
}

func (s *State) validateElasticsearchDraft(p connections.Profile) error {
	if p.TLSMode == "disable" && (p.ElasticsearchOptions.Auth != "none" || s.connectionSecret != "") {
		return fieldError("Elasticsearch TLS 模式", "SSL", "密码和 API key 需要 HTTPS；HTTP 仅用于无认证连接。")
	}
	cfg, err := connections.ElasticsearchProfileConfig(p)
	if err != nil {
		return fieldError("Elasticsearch 认证方式", "常规", "请检查 Elasticsearch 认证方式、用户名和证书设置")
	}
	if connections.ValidateElasticsearchCredential(cfg, s.connectionSecret, false) != nil {
		field := "密码"
		if p.ElasticsearchOptions.Auth == "api_key" {
			field = "Elasticsearch API key"
		}
		return fieldError(field, "常规", "请检查 Elasticsearch 凭据；API key 使用完整编码值")
	}
	return nil
}
