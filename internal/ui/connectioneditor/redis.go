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
			theme.Field(c, locales.Text("Redis 拓扑："), func() {
				previous := a.Topology
				if connectionFormSelect(c, &a.Topology, []string{"standalone", "cluster", "sentinel"}, "Redis 拓扑", fieldWidth, false).Changed() {
					if a.Topology == "sentinel" {
						if s.connectionPort == "6379" {
							s.connectionPort = "26379"
						}
						if a.SentinelTLSMode == "" {
							a.SentinelTLSMode = "disable"
						}
					} else if previous == "sentinel" && s.connectionPort == "26379" {
						s.connectionPort = "6379"
					}
					if a.Topology == "cluster" {
						s.connectionDraft.Database = "0"
					}
					if a.Topology != "sentinel" {
						a.MasterName = ""
						a.SentinelUsername = ""
						a.SentinelTLSMode = ""
						a.SentinelCAFile = ""
						a.SentinelServerName = ""
					}
					if a.Topology == "standalone" {
						a.Seeds = ""
					}
				}
			})
			if a.Topology != "standalone" {
				s.editorInputRow(c, "附加节点：", "Redis 附加节点", &a.Seeds, labelWidth, fieldWidth)
			}
			if a.Topology == "sentinel" {
				s.editorInputRow(c, "主节点名称：", "Sentinel 主节点名称", &a.MasterName, labelWidth, fieldWidth)
				s.editorInputRow(c, "Sentinel 用户名：", "Sentinel 用户名", &a.SentinelUsername, labelWidth, fieldWidth)
				theme.Field(c, locales.Text("Sentinel 密码："), func() { s.formTextInput(c, &s.redisSentinelPassword, "Sentinel 密码", fieldWidth, false, true) })
				connectionFormCheckbox(c, &s.redisSaveSentinelPassword, "保存 Sentinel 密码", s.credentialVault == nil)
			}
			s.editorInputRow(c, "连接超时（秒）", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "连接池上限", "连接池上限", &s.engineNumbers[1], labelWidth, 120)
			s.editorInputRow(c, "查询会话上限", "查询会话上限", &s.sqlMaxConnections, labelWidth, 120)
			ui.Text(c, locales.Text("Redis 7 及以上版本；附加节点使用 host:port，逗号分隔。Sentinel 主机和端口指向 Sentinel，Redis 与 Sentinel 密码独立。"))
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
			if a.Topology == "sentinel" {
				theme.Field(c, locales.Text("Sentinel TLS 模式："), func() {
					connectionFormSelect(c, &a.SentinelTLSMode, []string{"disable", "verify-full"}, "Sentinel TLS 模式", fieldWidth, false)
				})
				s.editorInputRow(c, "Sentinel CA 证书", "Sentinel CA 证书", &a.SentinelCAFile, labelWidth, fieldWidth)
				s.editorInputRow(c, "Sentinel 证书服务器名", "Sentinel 证书服务器名", &a.SentinelServerName, labelWidth, fieldWidth)
			}
		})
		return true
	}
	return false
}
