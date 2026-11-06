package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) mongoDBOptions(c *ui.Context, width float32, tab string) bool {
	a := &s.connectionDraft.MongoDBOptions
	labelWidth, fieldWidth := connectionFormLabelWidth, width-connectionFormLabelWidth
	if tab == "高级" {
		theme.Form(c, width, func() {
			if connectionFormCheckbox(c, &a.SRV, "MongoDB SRV（Atlas）", false).Changed() && a.SRV {
				s.connectionDraft.TLSMode = "verify-full"
			}
			connectionFormCheckbox(c, &a.DirectConnection, "直接连接单个 MongoDB 节点", false)
			s.editorInputRow(c, "附加节点：", "MongoDB 附加节点", &a.Seeds, labelWidth, fieldWidth)
			s.editorInputRow(c, "副本集名称：", "副本集名称", &a.ReplicaSet, labelWidth, fieldWidth)
			s.editorInputRow(c, "认证数据库：", "认证数据库", &a.AuthSource, labelWidth, fieldWidth)
			theme.Field(c, locales.Text("认证机制："), func() {
				connectionFormSelect(c, &a.AuthMechanism, []string{"", "SCRAM-SHA-1", "SCRAM-SHA-256"}, "MongoDB 认证机制", fieldWidth, false)
			})
			s.editorInputRow(c, "连接超时（秒）", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "连接池上限", "连接池上限", &s.engineNumbers[1], labelWidth, 120)
			s.editorInputRow(c, "查询会话上限", "查询会话上限", &s.sqlMaxConnections, labelWidth, 120)
			s.editorInputRow(c, "应用名称：", "应用名称", &a.ApplicationName, labelWidth, fieldWidth)
			ui.Text(c, locales.Text("附加节点使用逗号分隔的 host:port；SRV 使用单个域名和 TLS。"))
		})
		return true
	}
	if tab == "SSL" {
		theme.Form(c, width, func() {
			theme.Field(c, locales.Text("TLS 模式："), func() {
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "verify-full"}, "MongoDB TLS 模式", fieldWidth, false)
			})
			s.editorInputRow(c, "CA 证书", "MongoDB CA 证书", &a.CAFile, labelWidth, fieldWidth)
		})
		return true
	}
	return false
}
