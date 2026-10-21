package shell

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlSSLTester(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s := New()
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
