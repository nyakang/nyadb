package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// MySQLSSLFile is a file-selector destination (never file contents).
type MySQLSSLFile int

const (
	MySQLSSLClientKey MySQLSSLFile = iota
	MySQLSSLClientCertificate
	MySQLSSLCA
)

// The native file picker is owned by the desktop bootstrap layer.
func (s *State) SetMySQLSSLFilePicker(pick func(MySQLSSLFile)) {
	s.mysqlSSLFilePicker = pick
}

// SessionVersion guards against a chooser completing after the connection
// editor has been closed and reopened for a different profile.
func (s *State) MySQLSSLSessionVersion() uint64 {
	return s.mysqlSSLSessionVersion
}

func (s *State) SelectMySQLSSLFile(kind MySQLSSLFile, version uint64, path string) {
	if version != s.mysqlSSLSessionVersion || !s.connectionDialog ||
		s.connectionStep != 1 || s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionDraft.MySQLAdvanced.SSL.Enabled ||
		!s.connectionDraft.MySQLAdvanced.SSL.UseVerification || path == "" {
		return
	}
	switch kind {
	case MySQLSSLClientKey:
		s.connectionDraft.MySQLAdvanced.SSL.ClientKey = path
	case MySQLSSLClientCertificate:
		s.connectionDraft.MySQLAdvanced.SSL.ClientCertificate = path
	case MySQLSSLCA:
		s.connectionDraft.MySQLAdvanced.SSL.CACertificate = path
	default:
		return
	}
	s.connectionError = ""
}

func (s *State) MySQLSSLFilePickerError(version uint64) {
	if version == s.mysqlSSLSessionVersion && s.connectionDialog &&
		s.connectionStep == 1 && s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择 SSL 文件失败"
	}
}

func (s *State) clearMySQLSSLPassphrase() {
	s.mysqlSSLPassphrase = ""
	s.mysqlSSLSessionVersion++
}

// connectionMySQLSSL uses only MyGo native widgets and independent labels;
// paths are non-secret metadata, the password stays in memory.
func (s *State) connectionMySQLSSL(c *ui.Context, width float32) {
	formWidth := max(float32(520), width-40)
	labelWidth := float32(166)
	fieldWidth := formWidth - labelWidth
	ssl := &s.connectionDraft.MySQLAdvanced.SSL
	ui.Column(c).Width(formWidth).Gap(1).Children(func() {
		ui.Row(c).Height(28).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &ssl.Enabled, "使用 SSL").Label("使用 SSL")
		})
		ui.Row(c).Height(28).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &ssl.UseVerification, "使用验证").
				Label("使用验证").Disabled(!ssl.Enabled)
		})
		active := ssl.Enabled && ssl.UseVerification
		s.mysqlSSLPathRow(c, "客户端密钥：", &ssl.ClientKey, MySQLSSLClientKey,
			labelWidth, fieldWidth, active)
		s.mysqlSSLPathRow(c, "客户端证书：", &ssl.ClientCertificate, MySQLSSLClientCertificate,
			labelWidth, fieldWidth, active)
		s.mysqlSSLPathRow(c, "CA 证书：", &ssl.CACertificate, MySQLSSLCA,
			labelWidth, fieldWidth, active)
		ui.Row(c).Height(28).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Checkbox(c, &ssl.VerifyServerCA, "验证针对 CA 的服务器证书").
				Label("验证针对 CA 的服务器证书").Disabled(!active)
		})
		ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "指定密码：").FontSize(12).Width(labelWidth).
				TextColor(theme.Muted)
			ui.TextInput(c, &s.mysqlSSLPassphrase).
				Label("SSL 密钥密码").Password().Width(fieldWidth).
				Height(30).Padding(2, 8).Disabled(!active)
		})
		ui.Row(c).Height(26).Gap(0).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, "密钥密码仅保留在当前编辑窗口，不会保存或用于连接。").
				TextColor(theme.Muted).FontSize(11)
		})
	})
}

func (s *State) mysqlSSLPathRow(c *ui.Context, caption string, value *string,
	kind MySQLSSLFile, labelWidth, fieldWidth float32, active bool,
) {
	ui.Row(c).Height(32).Gap(0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, caption).Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		ui.TextInput(c, value).Label(caption).
			Width(fieldWidth-34).Height(30).Padding(2, 8).Disabled(!active)
		ui.Box(c).Width(4)
		if ui.Button(c, "…").Label("选择"+caption+"文件").
			Width(30).Height(30).Padding(0, 0).
			Disabled(!active || s.mysqlSSLFilePicker == nil).Clicked() {
			s.mysqlSSLFilePicker(kind)
		}
	})
}
