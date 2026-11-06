package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) redisOptions(c *ui.Context, width float32, tab string) bool {
	a := &s.connectionDraft.RedisOptions
	labelWidth, fieldWidth := connectionFormLabelWidth, width-connectionFormLabelWidth
	if tab == "高级" {
		theme.Form(c, width, func() {
			s.editorInputRow(c, "连接超时（秒）", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "连接池上限", "连接池上限", &s.engineNumbers[1], labelWidth, 120)
			s.editorInputRow(c, "查询会话上限", "查询会话上限", &s.sqlMaxConnections, labelWidth, 120)
			ui.Text(c, locales.Text("Redis 单节点连接；支持 Redis 7 及以上版本。用户名留空使用默认用户。"))
		})
		return true
	}
	if tab == "SSL" {
		theme.Form(c, width, func() {
			theme.Field(c, locales.Text("TLS 模式："), func() {
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "verify-full"}, "Redis TLS 模式", fieldWidth, false)
			})
			s.editorInputRow(c, "CA 证书", "Redis CA 证书", &a.CAFile, labelWidth, fieldWidth)
			s.editorInputRow(c, "证书服务器名", "证书服务器名", &a.ServerName, labelWidth, fieldWidth)
		})
		return true
	}
	return false
}
