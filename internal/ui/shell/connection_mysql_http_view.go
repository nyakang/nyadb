package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
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
		!s.connectionDialog || s.connectionStep != 1 ||
		s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionDraft.MySQLAdvanced.HTTP.Enabled ||
		!s.connectionDraft.MySQLAdvanced.HTTP.CertificateAuth {
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
	if version == s.mysqlHTTPSessionVersion && s.connectionDialog &&
		s.connectionStep == 1 && s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择 HTTP 隧道证书文件失败"
	}
}

func (s *State) connectionMySQLHTTP(c *ui.Context, width float32) {
	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	cfg := &s.connectionDraft.MySQLAdvanced.HTTP
	active := cfg.Enabled
	ui.Column(c).Width(formWidth).Gap(1).Children(func() {
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &cfg.Enabled, "使用 HTTP 隧道", s.connectionDraft.MySQLAdvanced.SSH.Enabled)
		})
		s.mysqlHTTPInput(c, "隧道网址：", &cfg.URL, labelWidth, fieldWidth, active)
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &cfg.Base64Queries, "使用 base64 编码传出查询", !active)
		})
		ui.Row(c).Height(29).Gap(0).Children(func() {
			tabs := ui.TabsBase(c, &s.mysqlHTTPSubtab, 2)
			tabs.List.Gap(0).Children(func() {
				for i, name := range []string{"验证", "代理"} {
					item := tabs.Tab(i).Height(29).Padding(4, 12).Border(1, theme.Border)
					if i == s.mysqlHTTPSubtab {
						item.Background(theme.Header)
					}
					item.Children(func() { ui.Text(c, name).FontSize(12) })
				}
			})
			ui.Box(c).Grow(1)
		})
		ui.Column(c).Width(formWidth).Height(365).Border(1, theme.Border).
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
		ui.Row(c).Height(32).Justify(ui.End).AlignItems(ui.Center).Children(func() {
			ui.Button(c, "导出隧道脚本…").Width(200).Height(30).
				Label("导出隧道脚本（尚未实现）").Disabled(true)
		})
	})
}

func (s *State) mysqlHTTPVerification(c *ui.Context, cfg *connections.MySQLHTTP,
	labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, &cfg.PasswordAuth, "使用密码验证", !active)
	})
	passwordEnabled := active && cfg.PasswordAuth
	s.mysqlHTTPInput(c, "用户名：", &cfg.Username, labelWidth, 200, passwordEnabled)
	s.mysqlHTTPPasswordRow(c, "密码：", &s.mysqlHTTPPassword, labelWidth, 200, passwordEnabled)
	s.mysqlHTTPSavePlaceholder(c, "保存密码", labelWidth)
	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, &cfg.CertificateAuth, "使用证书验证", !active)
	})
	certEnabled := active && cfg.CertificateAuth
	s.mysqlHTTPFileRow(c, "客户端密钥：", &cfg.ClientKey, MySQLHTTPClientKey,
		labelWidth, fieldWidth, certEnabled)
	s.mysqlHTTPFileRow(c, "客户端证书：", &cfg.ClientCertificate, MySQLHTTPClientCertificate,
		labelWidth, fieldWidth, certEnabled)
	s.mysqlHTTPFileRow(c, "CA 证书：", &cfg.CACertificate, MySQLHTTPCA,
		labelWidth, fieldWidth, certEnabled)
	s.mysqlHTTPPasswordRow(c, "通行短语：", &s.mysqlHTTPPassphrase, labelWidth, fieldWidth-34, certEnabled)
}

func (s *State) mysqlHTTPProxy(c *ui.Context, cfg *connections.MySQLHTTP,
	labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		connectionFormCheckbox(c, &cfg.ProxyEnabled, "使用代理", !active)
	})
	proxyActive := active && cfg.ProxyEnabled
	s.mysqlHTTPInput(c, "主机：", &cfg.ProxyHost, labelWidth, fieldWidth, proxyActive)
	connectionFormRow(c).Children(func() {
		ui.Text(c, "端口：").Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		connectionFormTextInput(c, &s.mysqlHTTPProxyPort, "HTTP 代理端口", 80, !proxyActive, false)
	})
	s.mysqlHTTPInput(c, "用户名：", &cfg.ProxyUsername, labelWidth, 200, proxyActive)
	s.mysqlHTTPPasswordRow(c, "密码：", &s.mysqlHTTPProxyPassword, labelWidth, 200, proxyActive)
	s.mysqlHTTPSavePlaceholder(c, "保存密码", labelWidth)
}

func (s *State) mysqlHTTPInput(c *ui.Context, name string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth, !active, false)
	})
}

func (s *State) mysqlHTTPPasswordRow(c *ui.Context, name string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth, !active, true)
	})
}

func (s *State) mysqlHTTPSavePlaceholder(c *ui.Context, name string, labelWidth float32) {
	ui.Row(c).Height(27).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(labelWidth)
		save := false
		connectionFormCheckbox(c, &save, name, true)
	})
}

func (s *State) mysqlHTTPFileRow(c *ui.Context, name string, value *string,
	kind MySQLHTTPFile, labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, name).Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		connectionFormTextInput(c, value, "HTTP "+name, fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, false)
		ui.Box(c).Width(connectionFormBrowseGap)
		connectionFormBrowse(c, "选择 HTTP "+name+"文件", !active || s.mysqlHTTPFilePicker == nil, func() { s.mysqlHTTPFilePicker(kind) })
	})
}
