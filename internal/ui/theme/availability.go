package theme

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func AvailabilityText(a contract.Availability) string {
	if a.Enabled {
		return ""
	}
	switch a.Reason {
	case "disconnected":
		return locales.Text("请先连接原连接")
	case "invalid_target", "missing_target":
		return locales.Text("请先选择适用的数据库对象")
	case "read_only":
		return locales.Text("当前对象为只读")
	case "permission":
		return locales.Text("当前账号权限不足")
	case "version":
		return locales.Text("当前服务器版本不支持此操作")
	case "not_implemented":
		return locales.Text("此功能尚未实现")
	default:
		return locales.Text("当前引擎不支持此操作")
	}
}
