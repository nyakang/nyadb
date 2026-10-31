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
	if version != s.mysqlSSLSessionVersion || !s.active ||
		s.connectionDraft.Kind != connections.MySQL ||
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
	if version == s.mysqlSSLSessionVersion && s.active &&
		s.connectionDraft.Kind == connections.MySQL {
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
	palette := theme.Colors(c)

	formWidth := connectionWideFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	ssl := &s.connectionDraft.MySQLAdvanced.SSL
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &ssl.Enabled, "使用 SSL", false)
		})
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &ssl.UseVerification, "使用验证", !ssl.Enabled)
		})
		active := ssl.Enabled && ssl.UseVerification
		s.mysqlSSLPathRow(c, "客户端密钥：", &ssl.ClientKey, MySQLSSLClientKey,
			labelWidth, fieldWidth, active)
		s.mysqlSSLPathRow(c, "客户端证书：", &ssl.ClientCertificate, MySQLSSLClientCertificate,
			labelWidth, fieldWidth, active)
		s.mysqlSSLPathRow(c, "CA 证书：", &ssl.CACertificate, MySQLSSLCA,
			labelWidth, fieldWidth, active)
		connectionFormRow(c).Children(func() {
			ui.Box(c).Width(labelWidth)
			connectionFormCheckbox(c, &ssl.VerifyServerCA, "验证针对 CA 的服务器证书", !active)
		})
		connectionFormRow(c).Children(func() {
			ui.Text(c, locales.Text("指定密码：")).FontSize(theme.Font(c, 12)).Width(labelWidth).
				TextColor(palette.Muted)
			connectionFormTextInput(c, &s.mysqlSSLPassphrase, "SSL 密钥密码", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, true)
		})
		ui.Row(c).Height(theme.RowHeight(c, 26)).Gap(0).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, locales.Text("密钥密码仅保留在当前编辑窗口，不会保存或用于连接。")).
				TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
		ui.Row(c).Height(theme.RowHeight(c, 22)).Gap(0).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, "TLS 连接始终校验证书链和主机名，不允许关闭验证。").TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
	})
}

func (s *State) mysqlSSLPathRow(c *ui.Context, caption string, value *string,
	kind MySQLSSLFile, labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		ui.Text(c, caption).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, value, "SSL "+caption, fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, false)
		ui.Box(c).Width(connectionFormBrowseGap)
		connectionFormBrowse(c, "选择"+caption+"文件", !active || s.mysqlSSLFilePicker == nil, func() { s.mysqlSSLFilePicker(kind) })
	})
}

// SSH credentials remain in the current editing session only.
// Profile stores the tunnel endpoint, authentication choice and key file path.
func (s *State) initMySQLSSHInputs() {
	ssh := s.connectionDraft.MySQLAdvanced.SSH
	if ssh == (connections.MySQLSSH{}) {
		ssh = connections.DefaultMySQLSSH()
		s.connectionDraft.MySQLAdvanced.SSH = ssh
	}
	s.mysqlSSHPort = strconv.Itoa(ssh.Port)
	s.clearMySQLSSHSecrets()
}

func (s *State) clearMySQLSSHSecrets() {
	s.mysqlSSHPassword = ""
	s.mysqlSSHPassphrase = ""
	s.mysqlSSHSessionVersion++
}

func (s *State) advanceMySQLSSH(p *connections.Profile) error {
	if p.Kind != connections.MySQL {
		return nil
	}
	port, err := strconv.ParseUint(strings.TrimSpace(s.mysqlSSHPort), 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("SSH 端口必须是 1–65535 之间的整数")
	}
	p.MySQLAdvanced.SSH.Port = int(port)
	return nil
}

func (s *State) SetMySQLSSHKeyPicker(pick func()) {
	s.mysqlSSHKeyPicker = pick
}

func (s *State) MySQLSSHSessionVersion() uint64 {
	return s.mysqlSSHSessionVersion
}

func (s *State) SelectMySQLSSHPrivateKey(version uint64, path string) {
	if version != s.mysqlSSHSessionVersion ||
		!s.active || s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionDraft.MySQLAdvanced.SSH.Enabled ||
		s.connectionDraft.MySQLAdvanced.SSH.AuthMethod == connections.MySQLSSHPassword ||
		path == "" {
		return
	}
	s.connectionDraft.MySQLAdvanced.SSH.PrivateKey = path
	s.connectionError = ""
}

func (s *State) MySQLSSHKeyPickerError(version uint64) {
	if version == s.mysqlSSHSessionVersion && s.active &&
		s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择 SSH 私钥文件失败"
	}
}

// connectionMySQLSSH mirrors the original's conditional password/key fields.
// Authentication stays separate from non-secret profile metadata.
func (s *State) connectionMySQLSSH(c *ui.Context, width float32) {
	palette := theme.Colors(c)

	formWidth := connectionWideFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	ssh := &s.connectionDraft.MySQLAdvanced.SSH
	active := ssh.Enabled
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			connectionFormCheckbox(c, &ssh.Enabled, "使用 SSH 隧道", s.connectionDraft.MySQLAdvanced.HTTP.Enabled)
		})
		s.mysqlSSHInputRow(c, "主机：", &ssh.Host, labelWidth, fieldWidth, active)
		connectionFormRow(c).Children(func() {
			ui.Text(c, locales.Text("端口：")).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
			connectionFormTextInput(c, &s.mysqlSSHPort, "SSH 端口", 80, !active, false)
		})
		s.mysqlSSHInputRow(c, "用户名：", &ssh.Username, labelWidth, 200, active)
		connectionFormRow(c).Children(func() {
			ui.Text(c, locales.Text("验证方法：")).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
			connectionFormSelect(c, &ssh.AuthMethod, connections.MySQLSSHAuthMethods, "SSH 验证方法", 200, !active)
		})
		passwordMode := ssh.AuthMethod != connections.MySQLSSHPublicKey
		keyMode := ssh.AuthMethod != connections.MySQLSSHPassword
		if passwordMode {
			connectionFormRow(c).Children(func() {
				ui.Text(c, locales.Text("密码：")).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
				connectionFormTextInput(c, &s.mysqlSSHPassword, "SSH 密码", 200, !active, true)
			})
		}
		if keyMode {
			connectionFormRow(c).Children(func() {
				ui.Text(c, "Private Key:").Width(labelWidth).FontSize(theme.Font(c, 12))
				connectionFormTextInput(c, &ssh.PrivateKey, "SSH 私钥文件", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, false)
				ui.Box(c).Width(connectionFormBrowseGap)
				connectionFormBrowse(c, "选择 SSH 私钥", !active || s.mysqlSSHKeyPicker == nil, s.mysqlSSHKeyPicker)
			})
			connectionFormRow(c).Children(func() {
				ui.Text(c, "Passphrase:").Width(labelWidth).FontSize(theme.Font(c, 12))
				connectionFormTextInput(c, &s.mysqlSSHPassphrase, "SSH 私钥口令", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, true)
			})
		}
		connectionFormRow(c).Children(func() {
			ui.Box(c).Width(labelWidth)
			connectionFormCheckbox(c, &s.mysqlSaveTunnelSecrets, "在系统密钥库保存隧道凭据", s.tunnelSecrets == nil || !active)
		})
		ui.Row(c).Height(theme.RowHeight(c, 25)).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, "首次连接需确认 SHA-256 主机指纹；后续指纹变化将拒绝连接。").
				TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
	})
}

func (s *State) mysqlSSHInputRow(
	c *ui.Context, title string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	palette := theme.Colors(c)

	connectionFormRow(c).Children(func() {
		ui.Text(c, title).Width(labelWidth).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		connectionFormTextInput(c, value, "SSH "+title, fieldWidth, !active, false)
	})
}
