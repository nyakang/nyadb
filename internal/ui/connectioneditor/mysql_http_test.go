package connectioneditor

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlHTTPTester(t *testing.T) (*testEditor, *ui.Tester) {
	t.Helper()
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "HTTP settings"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	return s, tt
}

func TestMySQLHTTPNativeControlsAndProxy(t *testing.T) {
	s, tt := mysqlHTTPTester(t)
	cfg := &s.connectionDraft.MySQLAdvanced.HTTP
	if cfg.Enabled || cfg.ProxyPort != 8080 || cfg.PasswordAuth ||
		!tt.HasText("使用 HTTP 隧道") ||
		!tt.HasText("隧道网址：") || !tt.HasText("使用 base64 编码传出查询") ||
		!tt.HasText("使用密码验证") || !tt.HasText("使用证书验证") {
		t.Fatalf("HTTP defaults and labels differ: %+v", cfg)
	}
	_ = tt.Click("使用密码验证")
	if cfg.PasswordAuth {
		t.Fatal("password auth should stay disabled before tunnel is enabled")
	}
	if err := tt.Click("使用 HTTP 隧道"); err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || !tt.HasText("HTTP 服务器") {
		t.Fatal("HTTP diagram or switch missing")
	}
	cfg.URL = "https://example.test/tunnel.php"
	if err := tt.Click("使用 base64 编码传出查询"); err != nil {
		t.Fatal(err)
	}
	if !cfg.Base64Queries {
		t.Fatal("base64 switch did not update")
	}
	if err := tt.Click("使用密码验证"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用证书验证"); err != nil {
		t.Fatal(err)
	}
	if !cfg.PasswordAuth || !cfg.CertificateAuth {
		t.Fatal("authentication sub-switches did not update")
	}
	called := MySQLHTTPFile(-1)
	s.SetMySQLHTTPFilePicker(func(f MySQLHTTPFile) { called = f })
	_ = tt.Click("选择 HTTP 客户端密钥：文件")
	if called != MySQLHTTPClientKey {
		t.Fatalf("file picker callback mismatch: %v", called)
	}
	version := s.MySQLHTTPSessionVersion()
	s.SelectMySQLHTTPFile(MySQLHTTPClientKey, version, "/tmp/http-key")
	s.SelectMySQLHTTPFile(MySQLHTTPClientCertificate, version, "/tmp/http-cert")
	s.SelectMySQLHTTPFile(MySQLHTTPCA, version, "/tmp/http-ca")
	if cfg.ClientKey != "/tmp/http-key" || cfg.ClientCertificate != "/tmp/http-cert" ||
		cfg.CACertificate != "/tmp/http-ca" {
		t.Fatalf("certificate paths not assigned: %+v", cfg)
	}
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("使用代理") || s.mysqlHTTPSubtab != 1 {
		t.Fatal("native proxy tab is missing")
	}
	if err := tt.Click("使用代理"); err != nil {
		t.Fatal(err)
	}
	if !cfg.ProxyEnabled {
		t.Fatal("proxy switch did not update")
	}
	cfg.ProxyHost = "proxy.example.test"
	cfg.ProxyUsername = "user"
	s.mysqlHTTPProxyPort = "0"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("invalid proxy port was accepted")
	}
	s.mysqlHTTPProxyPort = "8081"
	if p, err := s.draftProfile(); err != nil || p.MySQLAdvanced.HTTP.ProxyPort != 8081 {
		t.Fatalf("proxy port missing in draft: %+v (%v)", p.MySQLAdvanced.HTTP, err)
	}
	s.mysqlHTTPPassword = "private"
	s.mysqlHTTPProxyPassword = "proxy-secret"
	s.mysqlHTTPPassphrase = "phrase"
	s.closeConnectionDialog()
	if s.mysqlHTTPPassword != "" || s.mysqlHTTPProxyPassword != "" ||
		s.mysqlHTTPPassphrase != "" {
		t.Fatal("closing HTTP editor did not clear secrets")
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.SelectMySQLHTTPFile(MySQLHTTPClientKey, version, "/tmp/stale")
	if s.connectionDraft.MySQLAdvanced.HTTP.ClientKey != "" {
		t.Fatal("stale file dialog modified a new session")
	}
}

func TestMySQLHTTPTunnelMutualExclusion(t *testing.T) {
	s, tt := mysqlHTTPTester(t)
	if err := tt.Click("使用 HTTP 隧道"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("SSH"); err != nil {
		t.Fatal(err)
	}
	_ = tt.Click("使用 SSH 隧道")
	if s.connectionDraft.MySQLAdvanced.SSH.Enabled {
		t.Fatal("SSH enabled while HTTP was already enabled")
	}
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用 HTTP 隧道"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("SSH"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用 SSH 隧道"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDraft.MySQLAdvanced.SSH.Enabled {
		t.Fatal("SSH checkbox not enabled")
	}
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	_ = tt.Click("使用 HTTP 隧道")
	if s.connectionDraft.MySQLAdvanced.HTTP.Enabled {
		t.Fatal("HTTP enabled while SSH was already enabled")
	}
}

func TestMySQLHTTPSaveWithoutCredentials(t *testing.T) {
	s, _, service, path := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "http metadata"
	cfg := &s.connectionDraft.MySQLAdvanced.HTTP
	*cfg = connections.MySQLHTTP{
		Enabled: true, URL: "https://example.test/tunnel.php",
		PasswordAuth: true, Username: "alice",
		CertificateAuth: true, ClientKey: "/tmp/client.key", ProxyPort: 8080,
	}
	s.mysqlHTTPPassword = "http-secret-never-save"
	s.mysqlHTTPProxyPassword = "proxy-secret-never-save"
	s.mysqlHTTPPassphrase = "keyphrase-never-save"
	s.saveConnectionDraft()
	if s.connectionDialog || s.mysqlHTTPPassword != "" || s.mysqlHTTPProxyPassword != "" ||
		s.mysqlHTTPPassphrase != "" {
		t.Fatalf("save failed to clear secrets: %s", s.connectionError)
	}
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || profiles[0].MySQLAdvanced.HTTP.URL != cfg.URL {
		t.Fatalf("saved HTTP configuration missing: %+v %v", profiles, err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"http-secret-never-save", "proxy-secret-never-save", "keyphrase-never-save"} {
		if strings.Contains(string(bytes), secret) {
			t.Fatalf("saved HTTP secret in SQLite: %s", secret)
		}
	}
	s.startEditProfile()
	if s.connectionDraft.MySQLAdvanced.HTTP.ClientKey != cfg.ClientKey ||
		s.mysqlHTTPPassword != "" || s.mysqlHTTPProxyPassword != "" || s.mysqlHTTPPassphrase != "" {
		t.Fatal("edit profile restored credentials or lost metadata")
	}
	s.mysqlHTTPPassword = "closing-secret"
	s.ConnectionWindowClosed()
	if s.mysqlHTTPPassword != "" {
		t.Fatal("OS close did not clear HTTP password")
	}
}
