package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) oracleOptions(c *ui.Context, width float32, tab string) bool {
	a := &s.connectionDraft.OracleOptions
	labelWidth, fieldWidth := connectionFormLabelWidth, width-connectionFormLabelWidth
	if tab == "高级" {
		theme.Form(c, width, func() {
			s.editorInputRow(c, "连接超时（秒）", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "后台连接上限", "后台连接上限", &s.engineNumbers[1], labelWidth, 120)
			s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
			s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
			s.editorInputRow(c, "应用名称：", "应用名称", &a.ApplicationName, labelWidth, fieldWidth)
		})
		return true
	}
	if tab == "SSL" {
		theme.Form(c, width, func() {
			theme.Field(c, locales.Text("TLS 模式："), func() {
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "require", "verify-full"}, "Oracle TLS 模式", fieldWidth, false)
			})
			s.editorInputRow(c, "CA 证书", "Oracle CA 证书", &a.CAFile, labelWidth, fieldWidth)
			s.editorInputRow(c, "证书服务器名", "证书服务器名", &a.ServerName, labelWidth, fieldWidth)
			ui.Text(c, locales.Text("verify-full 校验服务器证书；require 仅加密，disable 关闭 TLS。"))
		})
		return true
	}
	return false
}
