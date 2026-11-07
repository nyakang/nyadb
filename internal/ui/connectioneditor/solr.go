package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) setSolrAuth(auth string) {
	if s.connectionDraft.Kind != connections.Solr || auth == s.connectionDraft.SolrOptions.Auth || auth != "none" && auth != "basic" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.SolrOptions.Auth = auth
	s.solrAuthChoice = solrAuthCaption(auth)
	s.connectionDraft.Username = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func solrAuthCaption(auth string) string {
	if auth == "basic" {
		return "用户名和密码"
	}
	return "无认证"
}

func (s *State) editorSolrFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	s.editorInputRow(c, "服务路径：", "Solr 服务路径", &p.SolrOptions.ContextPath, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.solrAuthChoice, []string{"无认证", "用户名和密码"}, "Solr 认证方式", fieldWidth, false)
	})
	if s.solrAuthChoice == "用户名和密码" {
		s.setSolrAuth("basic")
	} else {
		s.setSolrAuth("none")
	}
	if p.SolrOptions.Auth == "none" {
		return
	}
	s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("密码："), func() {
		s.formTextInput(c, &s.connectionSecret, "密码", fieldWidth, false, true)
	})
	ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() {
		connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil)
	})
}

func (s *State) solrSSL(c *ui.Context, width float32, tab string) bool {
	if tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.SolrOptions
	theme.Form(c, formWidth, func() {
		ui.Text(c, locales.Text("Solr 默认端口 8983，服务路径默认为 /solr。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Solr TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Solr CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		ui.Text(c, locales.Text("Solr 密码需要 HTTPS；HTTP 仅用于无认证连接。"))
	})
	return true
}

func (s *State) validateSolrDraft(p connections.Profile) error {
	if p.TLSMode == "disable" && (p.SolrOptions.Auth != "none" || s.connectionSecret != "") {
		return fieldError("Solr TLS 模式", "SSL", "Solr 密码需要 HTTPS；HTTP 仅用于无认证连接。")
	}
	if _, err := connections.NormalizeSolrOptions(p.SolrOptions); err != nil {
		return fieldError("Solr 服务路径", "常规", "请检查 Solr 服务路径、认证方式和证书设置")
	}
	cfg, err := connections.SolrProfileConfig(p)
	if err != nil {
		return fieldError("Solr 认证方式", "常规", "请检查 Solr 服务路径、认证方式和证书设置")
	}
	if connections.ValidateSolrCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("密码", "常规", "请检查 Solr 密码")
	}
	return nil
}
