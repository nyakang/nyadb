package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) setEasysearchAuth(auth string) {
	if s.connectionDraft.Kind != connections.Easysearch || auth == s.connectionDraft.EasysearchOptions.Auth || auth != "none" && auth != "basic" && auth != "access_token" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.EasysearchOptions.Auth = auth
	s.easysearchAuthChoice = easysearchAuthCaption(auth)
	if auth != "basic" {
		s.connectionDraft.Username = ""
	}
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func easysearchAuthCaption(auth string) string {
	switch auth {
	case "none":
		return "无认证"
	case "access_token":
		return "访问令牌"
	default:
		return "用户名和密码"
	}
}

func (s *State) editorEasysearchFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.easysearchAuthChoice, []string{"用户名和密码", "访问令牌", "无认证"}, "Easysearch 认证方式", fieldWidth, false)
	})
	switch s.easysearchAuthChoice {
	case "用户名和密码":
		s.setEasysearchAuth("basic")
	case "访问令牌":
		s.setEasysearchAuth("access_token")
	case "无认证":
		s.setEasysearchAuth("none")
	}
	if p.EasysearchOptions.Auth == "none" {
		return
	}
	if p.EasysearchOptions.Auth == "basic" {
		s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
	}
	caption, label := "密码：", "密码"
	if p.EasysearchOptions.Auth == "access_token" {
		caption, label = "访问令牌：", "Easysearch 访问令牌"
	}
	theme.Field(c, locales.Text(caption), func() {
		s.formTextInput(c, &s.connectionSecret, label, fieldWidth, false, true)
	})
	ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() {
		connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil)
	})
}

func (s *State) easysearchSSL(c *ui.Context, width float32, tab string) bool {
	if tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.EasysearchOptions
	theme.Form(c, formWidth, func() {
		ui.Text(c, locales.Text("Easysearch 使用 REST 接口，默认端口 9200。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Easysearch TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Easysearch CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		ui.Text(c, locales.Text("密码和访问令牌需要 HTTPS；HTTP 仅用于无认证连接。"))
	})
	return true
}

func (s *State) validateEasysearchDraft(p connections.Profile) error {
	if p.TLSMode == "disable" && (p.EasysearchOptions.Auth != "none" || s.connectionSecret != "") {
		return fieldError("Easysearch TLS 模式", "SSL", "密码和访问令牌需要 HTTPS；HTTP 仅用于无认证连接。")
	}
	cfg, err := connections.EasysearchProfileConfig(p)
	if err != nil {
		return fieldError("Easysearch 认证方式", "常规", "请检查 Easysearch 认证方式、用户名和证书设置")
	}
	if connections.ValidateEasysearchCredential(cfg, s.connectionSecret, false) != nil {
		field := "密码"
		if p.EasysearchOptions.Auth == "access_token" {
			field = "Easysearch 访问令牌"
		}
		return fieldError(field, "常规", "请检查 Easysearch 凭据；访问令牌使用原生完整值")
	}
	return nil
}
