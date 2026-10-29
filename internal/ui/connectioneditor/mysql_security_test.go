package connectioneditor

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlSSLTester(t *testing.T) (*testEditor, *ui.Tester) {
	t.Helper()
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	return s, tt
}

func TestMySQLSSLControlsAndFilePicker(t *testing.T) {
	s, tt := mysqlSSLTester(t)
	a := &s.connectionDraft.MySQLAdvanced.SSL
	if a.Enabled || a.UseVerification || a.VerifyServerCA ||
		!tt.HasText("客户端密钥：") || !tt.HasText("客户端证书：") ||
		!tt.HasText("CA 证书：") || !tt.HasText("指定密码：") {
		t.Fatalf("SSL page defaults or fields missing: %+v", a)
	}
	// Verification stays disabled until SSL is enabled.
	_ = tt.Click("使用验证")
	if a.UseVerification {
		t.Fatal("verification changed while SSL was disabled")
	}
	if err := tt.Click("使用 SSL"); err != nil {
		t.Fatal(err)
	}
	if !a.Enabled {
		t.Fatal("SSL checkbox did not enable")
	}
	_ = tt.Click("验证针对 CA 的服务器证书")
	if a.VerifyServerCA {
		t.Fatal("CA verification enabled before verification switch")
	}
	if err := tt.Click("使用验证"); err != nil {
		t.Fatal(err)
	}
	if !a.UseVerification {
		t.Fatal("verification checkbox did not enable")
	}
	if err := tt.Click("验证针对 CA 的服务器证书"); err != nil {
		t.Fatal(err)
	}
	if !a.VerifyServerCA {
		t.Fatal("CA verification did not enable")
	}

	called := MySQLSSLFile(-1)
	s.SetMySQLSSLFilePicker(func(k MySQLSSLFile) { called = k })
	_ = tt.Click("选择客户端密钥：文件")
	if called != MySQLSSLClientKey {
		t.Fatalf("incorrect key picker: %v", called)
	}
	version := s.MySQLSSLSessionVersion()
	s.SelectMySQLSSLFile(MySQLSSLClientKey, version, "/tmp/client.key")
	s.SelectMySQLSSLFile(MySQLSSLClientCertificate, version, "/tmp/client.crt")
	s.SelectMySQLSSLFile(MySQLSSLCA, version, "/tmp/ca.pem")
	if a.ClientKey != "/tmp/client.key" || a.ClientCertificate != "/tmp/client.crt" ||
		a.CACertificate != "/tmp/ca.pem" {
		t.Fatalf("file picker paths not assigned: %+v", a)
	}
	s.mysqlSSLPassphrase = "confidential"
	if err := tt.Click("使用验证"); err != nil {
		t.Fatal(err)
	}
	if a.UseVerification == true || !a.VerifyServerCA || a.ClientKey == "" {
		t.Fatalf("disabling verification should keep selections but deactivate inputs: %+v", a)
	}
	s.SelectMySQLSSLFile(MySQLSSLCA, version, "/tmp/should-not-change.pem")
	if a.CACertificate != "/tmp/ca.pem" {
		t.Fatal("file selection must not update disabled fields")
	}
	s.closeConnectionDialog()
	if s.mysqlSSLPassphrase != "" {
		t.Fatal("closing must clear SSL passphrase")
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.SelectMySQLSSLFile(MySQLSSLClientKey, version, "/tmp/stale.pem")
	if s.connectionDraft.MySQLAdvanced.SSL.ClientKey != "" {
		t.Fatal("stale file-picker result leaked into a new session")
	}
}

func TestMySQLSSLProfilePersistenceExcludesPassphrase(t *testing.T) {
	s, _, service, path := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "mysql tls metadata"
	a := &s.connectionDraft.MySQLAdvanced.SSL
	*a = connections.MySQLSSL{
		Enabled: true, UseVerification: true,
		ClientKey: "/tmp/test-key.pem", ClientCertificate: "/tmp/test-cert.pem",
		CACertificate: "/tmp/test-ca.pem", VerifyServerCA: true,
	}
	s.mysqlSSLPassphrase = "dont-write-me-to-disk"
	s.saveConnectionDraft()
	if s.connectionDialog || s.mysqlSSLPassphrase != "" {
		t.Fatalf("save must close and clear SSL password: %q", s.connectionError)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || list[0].MySQLAdvanced.SSL.ClientKey != a.ClientKey {
		t.Fatalf("SSL config did not persist: %+v %v", list, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dont-write-me-to-disk") {
		t.Fatal("SSL secret written to local config")
	}
	s.startEditProfile()
	if s.mysqlSSLPassphrase != "" ||
		!s.connectionDraft.MySQLAdvanced.SSL.VerifyServerCA {
		t.Fatal("editing must restore config without the passphrase")
	}
}

func TestMySQLSSLPassphraseClearedOnBackAndOSClose(t *testing.T) {
	s, tt := mysqlSSLTester(t)
	s.mysqlSSLPassphrase = "from-back"
	beforeBack := s.MySQLSSLSessionVersion()
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlSSLPassphrase != "" || s.MySQLSSLSessionVersion() == beforeBack {
		t.Fatal("back must clear passphrase and invalidate file dialogs")
	}
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.mysqlSSLPassphrase = "from-titlebar-x"
	beforeClose := s.MySQLSSLSessionVersion()
	s.ConnectionWindowClosed()
	if s.mysqlSSLPassphrase != "" || s.connectionDialog ||
		s.MySQLSSLSessionVersion() == beforeClose {
		t.Fatal("native window close must clear passphrase and invalidate file dialogs")
	}
}

func mysqlSSHTester(t *testing.T) (*testEditor, *ui.Tester) {
	t.Helper()
	s := newTestEditor()
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
