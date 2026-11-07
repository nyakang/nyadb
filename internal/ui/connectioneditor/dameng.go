package connectioneditor

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) advanceDamengOptions(p *connections.Profile) error {
	a := &p.DamengOptions
	for _, field := range []struct {
		name, raw string
		value     *int
		max       int
	}{
		{"连接超时（秒）", s.engineNumbers[0], &a.ConnectTimeoutSeconds, 120},
		{"执行超时（秒）", s.engineNumbers[2], &a.OperationTimeoutSeconds, 120},
		{"后台连接上限", s.engineNumbers[1], &a.MaxConnections, 64},
		{"SQL 会话上限", s.sqlMaxConnections, &a.SQLMaxConnections, 64},
	} {
		n, err := strconv.Atoi(strings.TrimSpace(field.raw))
		if err != nil || n < 1 || n > field.max {
			return fieldError(field.name, "高级", "请输入有效范围内的整数")
		}
		*field.value = n
	}
	if a.LibraryFile == "" || !filepath.IsAbs(a.LibraryFile) {
		return fieldError("达梦原厂客户端库", "高级", "请选择已安装的达梦原厂客户端库，路径必须为绝对路径")
	}
	if p.Database == "" {
		return fieldError("初始数据库", "常规", "请输入此连接绑定的达梦数据库")
	}
	if p.Username == "" {
		return fieldError("用户名", "常规", "请输入达梦用户名")
	}
	if p.TLSMode != "disable" && (a.CertificateFile == "" || a.KeyFile == "") {
		return fieldError("达梦客户端证书", "SSL", "达梦 TLS 需要客户端证书和密钥")
	}
	cfg, err := connections.DamengProfileConfig(*p)
	if err != nil {
		return err
	}
	p.Host, p.Port, p.TLSMode, p.DamengOptions = cfg.Host, cfg.Port, cfg.TLSMode, cfg.Options
	return nil
}

func (s *State) damengFile(c *ui.Context, caption, label string, value *string, kind EngineFile, width float32) {
	theme.Field(c, locales.Text(caption), func() {
		ui.Row(c).Gap(0).FillWidth().AlignItems(ui.Center).Children(func() {
			s.formTextInput(c, value, label, width-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "选择 "+label, s.engineFilePicker == nil, func() {
				if s.engineFilePicker != nil {
					s.engineFilePicker(kind, -1, s.engineFileVersion)
				}
			})
		})
	})
}

func (s *State) damengOptions(c *ui.Context, width float32, tab string) bool {
	a := &s.connectionDraft.DamengOptions
	labelWidth, fieldWidth := connectionFormLabelWidth, width-connectionFormLabelWidth
	switch tab {
	case "高级":
		theme.Form(c, width, func() {
			s.damengFile(c, "原厂客户端库：", "达梦原厂客户端库", &a.LibraryFile, DamengLibraryFile, fieldWidth)
			s.editorInputRow(c, "默认模式：", "达梦默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
			s.editorInputRow(c, "连接超时（秒）：", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "执行超时（秒）：", "执行超时（秒）", &s.engineNumbers[2], labelWidth, 120)
			s.editorInputRow(c, "后台连接上限：", "后台连接上限", &s.engineNumbers[1], labelWidth, 120)
			s.editorInputRow(c, "SQL 会话上限：", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
			ui.Text(c, locales.Text("请选择本机已安装的原厂 libdmdpi 库；默认模式在每条物理连接初始化。"))
		})
	case "SSL":
		theme.Form(c, width, func() {
			theme.Field(c, locales.Text("TLS 模式："), func() {
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "verify-ca", "require", "disable"}, "达梦 TLS 模式", fieldWidth, false)
			})
			s.damengFile(c, "CA 证书", "达梦 CA 证书", &a.CAFile, DamengCAFile, fieldWidth)
			s.damengFile(c, "客户端证书", "达梦客户端证书", &a.CertificateFile, DamengCertificateFile, fieldWidth)
			s.damengFile(c, "客户端密钥", "达梦客户端密钥", &a.KeyFile, DamengKeyFile, fieldWidth)
			s.editorInputRow(c, "证书服务器名", "达梦证书服务器名", &a.ServerName, labelWidth, fieldWidth)
			ui.Text(c, locales.Text("verify-full 校验证书链和主机名；达梦 TLS 需要客户端证书和密钥。"))
		})
	default:
		return false
	}
	return true
}
