package shell

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlSSHTester(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "ssh UI test"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("SSH"); err != nil {
		t.Fatal(err)
	}
	return s, tt
}

func TestMySQLSSHNativeModeSwitchingAndSecretLifecycle(t *testing.T) {
	s, tt := mysqlSSHTester(t)
	cfg := &s.connectionDraft.MySQLAdvanced.SSH
	if cfg.Enabled || cfg.Port != 22 || cfg.AuthMethod != connections.MySQLSSHPassword ||
		!tt.HasText("使用 SSH 隧道") || !tt.HasText("验证方法：") ||
		!tt.HasText("密码：") || tt.HasText("Private Key:") {
		t.Fatalf("default password method UI incorrect: %+v", cfg)
	}
	s.mysqlSSHPassword = "temporary"
	_ = tt.Click("使用 SSH 隧道")
	if !cfg.Enabled || !tt.HasText("SSH 服务器") {
		t.Fatal("SSH switch must update the tunnel diagram")
	}
	s.mysqlSSHPort = "0"
	if _, err := s.draftProfile(); err == nil || !strings.Contains(err.Error(), "SSH 端口") {
		t.Fatalf("invalid SSH port was accepted: %v", err)
	}
	s.mysqlSSHPort = "2222"
	cfg.AuthMethod = connections.MySQLSSHPublicKey
	tt = ui.NewTester(s.ConnectionWindowView, 900, 700)
	if !tt.HasText("Private Key:") || !tt.HasText("Passphrase:") ||
		tt.HasText("密码：") || !tt.HasText("Save Passphrase") {
		t.Fatalf("public-key mode incorrect: key=%v passphrase=%v password=%v save=%v",
			tt.HasText("Private Key:"), tt.HasText("Passphrase:"),
			tt.HasText("密码："), tt.HasText("Save Passphrase"))
	}
	called := 0
	s.SetMySQLSSHKeyPicker(func() { called++ })
	_ = tt.Click("选择 SSH 私钥")
	if called != 1 {
		t.Fatalf("browse callback not invoked: %d", called)
	}
	ver := s.MySQLSSHSessionVersion()
	s.SelectMySQLSSHPrivateKey(ver, "/home/user/.ssh/id_ed25519")
	if cfg.PrivateKey != "/home/user/.ssh/id_ed25519" {
		t.Fatal("file chooser did not update SSH key metadata")
	}
	cfg.AuthMethod = connections.MySQLSSHPasswordAndKey
	tt = ui.NewTester(s.ConnectionWindowView, 900, 700)
	if !tt.HasText("密码：") || !tt.HasText("Private Key:") ||
		!tt.HasText("Save Passphrase") {
		t.Fatal("combined authentication should show password and key controls")
	}
	cfg.Host = "ssh.example.test"
	cfg.Username = "alice"
	p, err := s.draftProfile()
	if err != nil || p.MySQLAdvanced.SSH.Port != 2222 {
		t.Fatalf("SSH options missing in profile draft: %+v %v", p.MySQLAdvanced.SSH, err)
	}
	s.mysqlSSHPassphrase = "passphrase-only"
	s.closeConnectionDialog()
	if s.mysqlSSHPassword != "" || s.mysqlSSHPassphrase != "" {
		t.Fatal("close must clear both SSH secrets")
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.SelectMySQLSSHPrivateKey(ver, "/tmp/stale")
	if s.connectionDraft.MySQLAdvanced.SSH.PrivateKey != "" {
		t.Fatal("stale private-key dialog modified a new connection")
	}
}

func TestMySQLSSHProfileSaveDoesNotPersistSecrets(t *testing.T) {
	s, _, service, path := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "ssh metadata only"
	cfg := &s.connectionDraft.MySQLAdvanced.SSH
	*cfg = connections.MySQLSSH{
		Enabled: true, Host: "jump.example.test", Port: 22,
		Username: "alice", AuthMethod: connections.MySQLSSHPasswordAndKey,
		PrivateKey: "/home/alice/.ssh/id_ed25519",
	}
	s.mysqlSSHPort = "22"
	s.mysqlSSHPassword = "secret-password-not-on-disk"
	s.mysqlSSHPassphrase = "secret-passphrase-not-on-disk"
	s.saveConnectionDraft()
	if s.connectionDialog || s.mysqlSSHPassword != "" || s.mysqlSSHPassphrase != "" {
		t.Fatalf("save must clear credentials: %s", s.connectionError)
	}
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || profiles[0].MySQLAdvanced.SSH != *cfg {
		t.Fatalf("SSH metadata not persisted: %+v %v", profiles, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-password-not-on-disk") ||
		strings.Contains(string(raw), "secret-passphrase-not-on-disk") {
		t.Fatal("SSH credential appeared in configuration database")
	}
	s.startEditProfile()
	if s.mysqlSSHPassword != "" || s.mysqlSSHPassphrase != "" ||
		s.mysqlSSHPort != "22" || s.connectionDraft.MySQLAdvanced.SSH.Host != "jump.example.test" {
		t.Fatal("edit did not restore the non-secret SSH settings")
	}
	s.mysqlSSHPassword = "another-secret"
	s.ConnectionWindowClosed()
	if s.mysqlSSHPassword != "" || s.connectionDialog {
		t.Fatal("OS window close failed to clear SSH credentials")
	}
}
