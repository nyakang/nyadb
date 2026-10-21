package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

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
		!s.connectionDialog || s.connectionStep != 1 ||
		s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionDraft.MySQLAdvanced.SSH.Enabled ||
		s.connectionDraft.MySQLAdvanced.SSH.AuthMethod == connections.MySQLSSHPassword ||
		path == "" {
		return
	}
	s.connectionDraft.MySQLAdvanced.SSH.PrivateKey = path
	s.connectionError = ""
}

func (s *State) MySQLSSHKeyPickerError(version uint64) {
	if version == s.mysqlSSHSessionVersion && s.connectionDialog &&
		s.connectionStep == 1 && s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择 SSH 私钥文件失败"
	}
}

// connectionMySQLSSH mirrors the original's conditional password/key fields.
// Never suggest the SSH tunnel is active; the actual transport is not built.
func (s *State) connectionMySQLSSH(c *ui.Context, width float32) {
	formWidth := connectionFormWidth(width)
	labelWidth := connectionFormLabelWidth
	fieldWidth := formWidth - labelWidth
	ssh := &s.connectionDraft.MySQLAdvanced.SSH
	active := ssh.Enabled
	ui.Column(c).Width(formWidth).Gap(0).Children(func() {
		connectionFormRow(c).Children(func() {
			ui.Checkbox(c, &ssh.Enabled, "使用 SSH 隧道").Label("使用 SSH 隧道").
				Disabled(s.connectionDraft.MySQLAdvanced.HTTP.Enabled)
		})
		s.mysqlSSHInputRow(c, "主机：", &ssh.Host, labelWidth, fieldWidth, active)
		connectionFormRow(c).Children(func() {
			ui.Text(c, "端口：").Width(labelWidth).FontSize(12).TextColor(theme.Muted)
			connectionFormTextInput(c, &s.mysqlSSHPort, "SSH 端口", 80, !active, false)
		})
		s.mysqlSSHInputRow(c, "用户名：", &ssh.Username, labelWidth, 200, active)
		connectionFormRow(c).Children(func() {
			ui.Text(c, "验证方法：").Width(labelWidth).FontSize(12).TextColor(theme.Muted)
			connectionFormSelect(c, &ssh.AuthMethod, connections.MySQLSSHAuthMethods, "SSH 验证方法", 200, !active)
		})
		passwordMode := ssh.AuthMethod != connections.MySQLSSHPublicKey
		keyMode := ssh.AuthMethod != connections.MySQLSSHPassword
		if passwordMode {
			connectionFormRow(c).Children(func() {
				ui.Text(c, "密码：").Width(labelWidth).FontSize(12).TextColor(theme.Muted)
				connectionFormTextInput(c, &s.mysqlSSHPassword, "SSH 密码", 200, !active, true)
			})
			connectionFormRow(c).Children(func() {
				ui.Box(c).Width(labelWidth)
				save := false
				ui.Checkbox(c, &save, "保存密码").Label("保存密码（需要系统密钥库）").
					Disabled(true)
			})
		}
		if keyMode {
			connectionFormRow(c).Children(func() {
				ui.Text(c, "Private Key:").Width(labelWidth).FontSize(12)
				connectionFormTextInput(c, &ssh.PrivateKey, "SSH 私钥文件", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, false)
				ui.Box(c).Width(connectionFormBrowseGap)
				connectionFormBrowse(c, "选择 SSH 私钥", !active || s.mysqlSSHKeyPicker == nil, s.mysqlSSHKeyPicker)
			})
			connectionFormRow(c).Children(func() {
				ui.Text(c, "Passphrase:").Width(labelWidth).FontSize(12)
				connectionFormTextInput(c, &s.mysqlSSHPassphrase, "SSH 私钥口令", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, !active, true)
			})
			connectionFormRow(c).Children(func() {
				ui.Box(c).Width(labelWidth)
				save := false
				ui.Checkbox(c, &save, "Save Passphrase").
					Label("Save Passphrase（需要系统密钥库）").Disabled(true)
			})
		}
		ui.Row(c).Height(25).Children(func() {
			ui.Box(c).Width(labelWidth)
			ui.Text(c, "密码与密钥口令仅在当前窗口内保留；SSH 隧道尚未连接。").
				TextColor(theme.Muted).FontSize(11)
		})
	})
}

func (s *State) mysqlSSHInputRow(
	c *ui.Context, title string, value *string,
	labelWidth, fieldWidth float32, active bool,
) {
	connectionFormRow(c).Children(func() {
		ui.Text(c, title).Width(labelWidth).FontSize(12).TextColor(theme.Muted)
		connectionFormTextInput(c, value, "SSH "+title, fieldWidth, !active, false)
	})
}
