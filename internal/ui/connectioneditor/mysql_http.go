package connectioneditor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// MySQLHTTPFile identifies non-secret certificate/key paths selected through
// the desktop's native file chooser.
type MySQLHTTPFile int

const (
	MySQLHTTPClientKey MySQLHTTPFile = iota
	MySQLHTTPClientCertificate
	MySQLHTTPCA
)

func (s *State) initMySQLHTTPInputs() {
	cfg := s.connectionDraft.MySQLAdvanced.HTTP
	if cfg == (connections.MySQLHTTP{}) {
		cfg = connections.DefaultMySQLHTTP()
		s.connectionDraft.MySQLAdvanced.HTTP = cfg
	}
	s.mysqlHTTPProxyPort = strconv.Itoa(cfg.ProxyPort)
	s.mysqlHTTPSubtab = 0
	s.clearMySQLHTTPSecrets()
}

func (s *State) clearMySQLHTTPSecrets() {
	s.mysqlHTTPPassword = ""
	s.mysqlHTTPProxyPassword = ""
	s.mysqlHTTPPassphrase = ""
	s.mysqlHTTPSessionVersion++
}

func (s *State) advanceMySQLHTTP(p *connections.Profile) error {
	if p.Kind != connections.MySQL {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s.mysqlHTTPProxyPort), 10, 16)
	if err != nil || n == 0 {
		return fmt.Errorf("HTTP 代理端口必须是 1–65535 之间的整数")
	}
	p.MySQLAdvanced.HTTP.ProxyPort = int(n)
	return nil
}

func (s *State) SetMySQLHTTPFilePicker(fn func(MySQLHTTPFile)) {
	s.mysqlHTTPFilePicker = fn
}

func (s *State) MySQLHTTPSessionVersion() uint64 {
	return s.mysqlHTTPSessionVersion
}

func (s *State) SelectMySQLHTTPFile(kind MySQLHTTPFile, version uint64, path string) {
	if version != s.mysqlHTTPSessionVersion || path == "" ||
		!s.active || s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionDraft.MySQLAdvanced.HTTP.Enabled ||
		(!s.connectionDraft.MySQLAdvanced.HTTP.CertificateAuth && kind != MySQLHTTPCA) {
		return
	}
	cfg := &s.connectionDraft.MySQLAdvanced.HTTP
	switch kind {
	case MySQLHTTPClientKey:
		cfg.ClientKey = path
	case MySQLHTTPClientCertificate:
		cfg.ClientCertificate = path
	case MySQLHTTPCA:
		cfg.CACertificate = path
	default:
		return
	}
	s.connectionError = ""
}

func (s *State) MySQLHTTPFilePickerError(version uint64) {
	if version == s.mysqlHTTPSessionVersion && s.active &&
		s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择 HTTP 隧道证书文件失败"
	}
}

func (s *State) connectionMySQLHTTP(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	formWidth := connectionWideFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	cfg := &s.connectionDraft.MySQLAdvanced.HTTP
	active := cfg.Enabled
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &cfg.Enabled, "使用 HTTP 隧道", s.connectionDraft.MySQLAdvanced.SSH.Enabled)
		})
		s.mysqlHTTPInput(c, "隧道网址：", &cfg.URL, labelWidth, fieldWidth, active)
		s.mysqlHTTPInput(c, locales.Text("目标 ID："), &cfg.TargetID, labelWidth, fieldWidth, active)
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &cfg.Base64Queries, "使用 base64 编码传出查询", true)
			if cfg.Base64Queries {
				if ui.Button(c, "清除旧版 base64 设置").Disabled(!active).Clicked() {
					cfg.Base64Queries = false
				}
			}
		})
		ui.Row(c).Height(theme.RowHeight(c, 27)).Gap(0).Children(func() {
			tabs := ui.TabsBase(c, &s.mysqlHTTPSubtab, 2)
			tabs.List.Gap(0).Children(func() {
				for i, name := range []string{"验证", "代理"} {
					item := tabs.Tab(i).Height(theme.RowHeight(c, 27)).Padding(4, 12).Border(1, palette.Border)
					if i == s.mysqlHTTPSubtab {
						item.Background(palette.Header)
					}
					item.Children(func() { ui.Text(c, name).FontSize(theme.Font(c, 12)) })
				}
			})
			ui.Box(c).Grow(1)
		})
		ui.Column(c).Width(formWidth).Height(373).Border(1, palette.Border).
			Padding(8, 9).Gap(0).Children(func() {
			inside := formWidth - 20
			innerLabel := connectionFormLabelWidth
			innerField := inside - innerLabel
			if s.mysqlHTTPSubtab == 0 {
				s.mysqlHTTPVerification(c, cfg, innerLabel, innerField, active)
			} else {
				s.mysqlHTTPProxy(c, cfg, innerLabel, innerField, active)
			}
		})
		ui.Row(c).Height(32).Margin(4, 0, 0, 0).Justify(ui.End).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "nyadb-tcp-v1 网关：HTTPS 身份验证与数据库 TLS 独立。 ").FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		})
	})
}

func (s *State) mysqlHTTPVerification(c *ui.Context, cfg *connections.MySQLHTTP,
	labelWidth, fieldWidth float32, active bool,
) {
	s.mysqlHTTPPasswordRow(c, locales.Text("访问令牌："), &s.mysqlHTTPPassword, labelWidth, fieldWidth, active)
	s.mysqlHTTPSavePlaceholder(c, locales.Text("在系统密钥库保存隧道凭据"), labelWidth)
	s.mysqlHTTPFileRow(c, "CA 证书：", &cfg.CACertificate, MySQLHTTPCA, labelWidth, fieldWidth, active)

	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, &cfg.CertificateAuth, "使用证书验证", !active)
	})
	certEnabled := active && cfg.CertificateAuth
	s.mysqlHTTPFileRow(c, "客户端密钥：", &cfg.ClientKey, MySQLHTTPClientKey,
		labelWidth, fieldWidth, certEnabled)
	s.mysqlHTTPFileRow(c, "客户端证书：", &cfg.ClientCertificate, MySQLHTTPClientCertificate,
		labelWidth, fieldWidth, certEnabled)
	s.mysqlHTTPPasswordRow(c, "通行短语：", &s.mysqlHTTPPassphrase, labelWidth, fieldWidth-34, certEnabled)
}

func (s *State) mysqlHTTPProxy(c *ui.Context, cfg *connections.MySQLHTTP,
	labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, &cfg.ProxyEnabled, "使用代理", !active)
	})
	proxyActive := active && cfg.ProxyEnabled
	s.mysqlHTTPInput(c, "主机：", &cfg.ProxyHost, labelWidth, fieldWidth, proxyActive)
	connectionFormRow(c).Children(func() {
		ui.Text(c, locales.Text("端口：")).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, &s.mysqlHTTPProxyPort, "HTTP 代理端口", 80, !proxyActive, false)
	})
	s.mysqlHTTPInput(c, "用户名：", &cfg.ProxyUsername, labelWidth, 200, proxyActive)
	s.mysqlHTTPPasswordRow(c, "密码：", &s.mysqlHTTPProxyPassword, labelWidth, 200, proxyActive)
	s.mysqlHTTPSavePlaceholder(c, "保存密码", labelWidth)
}

func (s *State) mysqlHTTPInput(c *ui.Context, name string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth, !active, false)
	})
}

func (s *State) mysqlHTTPPasswordRow(c *ui.Context, name string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth, !active, true)
	})
}

func (s *State) mysqlHTTPSavePlaceholder(c *ui.Context, name string, labelWidth float32) {
	ui.Row(c).Height(theme.RowHeight(c, 27)).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(labelWidth)
		connectionFormCheckbox(c, &s.mysqlSaveTunnelSecrets, name, s.tunnelSecrets == nil || !s.connectionDraft.MySQLAdvanced.HTTP.Enabled)
	})
}

func (s *State) mysqlHTTPFileRow(c *ui.Context, name string, value *string,
	kind MySQLHTTPFile, labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, false)
		ui.Box(c).Width(connectionFormBrowseGap)
		connectionFormBrowse(c, "选择 HTTP "+name+"文件", !active || s.mysqlHTTPFilePicker == nil, func() { s.mysqlHTTPFilePicker(kind) })
	})
}
