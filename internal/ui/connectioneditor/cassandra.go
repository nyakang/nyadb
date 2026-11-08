package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func cassandraAuthCaption(auth string) string {
	if auth == "password" {
		return "用户名和密码"
	}
	return "无认证"
}

func (s *State) setCassandraAuth(auth string) {
	if s.connectionDraft.Kind != connections.Cassandra || auth == s.connectionDraft.CassandraOptions.Auth || auth != "none" && auth != "password" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.CassandraOptions.Auth = auth
	s.cassandraAuthChoice = cassandraAuthCaption(auth)
	s.connectionDraft.Username = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) editorCassandraFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	s.editorInputRow(c, "初始 Keyspace：", "Cassandra 初始 Keyspace", &p.Database, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.cassandraAuthChoice, []string{"无认证", "用户名和密码"}, "Cassandra 认证方式", fieldWidth, false)
	})
	if s.cassandraAuthChoice == "用户名和密码" {
		s.setCassandraAuth("password")
	} else {
		s.setCassandraAuth("none")
	}
	if p.CassandraOptions.Auth == "none" {
		return
	}
	s.editorInputRow(c, "用户名：", "用户名", &p.Username, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "密码", fieldWidth, false, true) })
	ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
}

func (s *State) cassandraSSL(c *ui.Context, width float32, tab string) bool {
	if tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.CassandraOptions
	theme.Form(c, formWidth, func() {
		ui.Text(c, locales.Text("Cassandra 默认端口 9042；初始 Keyspace 可留空。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Cassandra TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Cassandra CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
	})
	return true
}

func (s *State) validateCassandraDraft(p connections.Profile) error {
	cfg, err := connections.CassandraProfileConfig(p)
	if err != nil {
		return fieldError("Cassandra 认证方式", "常规", "请检查 Cassandra 主机、Keyspace、认证方式和证书设置")
	}
	if connections.ValidateCassandraCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("密码", "常规", "请检查 Cassandra 密码")
	}
	return nil
}
