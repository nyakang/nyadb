package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) setCouchDBAuth(auth string) {
	if s.connectionDraft.Kind != connections.CouchDB || auth == s.connectionDraft.CouchDBOptions.Auth || auth != "none" && auth != "basic" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.CouchDBOptions.Auth = auth
	s.couchdbAuthChoice = couchdbAuthCaption(auth)
	s.connectionDraft.Username = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func couchdbAuthCaption(auth string) string {
	if auth == "basic" {
		return "用户名和密码"
	}
	return "无认证"
}

func (s *State) editorCouchDBFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	s.editorInputRow(c, "服务路径：", "CouchDB 服务路径", &p.CouchDBOptions.ContextPath, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.couchdbAuthChoice, []string{"无认证", "用户名和密码"}, "CouchDB 认证方式", fieldWidth, false)
	})
	if s.couchdbAuthChoice == "用户名和密码" {
		s.setCouchDBAuth("basic")
	} else {
		s.setCouchDBAuth("none")
	}
	if p.CouchDBOptions.Auth == "none" {
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

func (s *State) couchdbSSL(c *ui.Context, width float32, tab string) bool {
	if tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.CouchDBOptions
	theme.Form(c, formWidth, func() {
		ui.Text(c, locales.Text("CouchDB 默认端口 5984，服务路径留空表示服务器根路径。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "CouchDB TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "CouchDB CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
	})
	return true
}

func (s *State) validateCouchDBDraft(p connections.Profile) error {
	if _, err := connections.NormalizeCouchDBOptions(p.CouchDBOptions); err != nil {
		return fieldError("CouchDB 服务路径", "常规", "请检查 CouchDB 服务路径、认证方式和证书设置")
	}
	cfg, err := connections.CouchDBProfileConfig(p)
	if err != nil {
		return fieldError("CouchDB 认证方式", "常规", "请检查 CouchDB 服务路径、认证方式和证书设置")
	}
	if connections.ValidateCouchDBCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("密码", "常规", "请检查 CouchDB 密码")
	}
	return nil
}
