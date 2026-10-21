package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLAdvancedUIControlsAndValidation(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "my-local"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	adv := &s.connectionDraft.MySQLAdvanced
	if adv.ClientDriver != "默认" || adv.Charset != "自动" ||
		adv.KeepAlive || adv.KeepAliveSeconds != 240 ||
		!adv.ConnectTimeout || adv.ConnectTimeoutSeconds != 30 ||
		adv.ReadTimeout || !adv.WriteTimeout ||
		!tt.HasText("设置位置：") || !tt.HasText("自动连接") ||
		!tt.HasText("使用命名管道，套接字：") {
		t.Fatalf("advanced defaults and controls do not match original: %+v", adv)
	}
	if err := tt.Click("保持连接间隔（秒）："); err != nil {
		t.Fatal(err)
	}
	if !adv.KeepAlive {
		t.Fatal("keep-alive checkbox did not toggle")
	}
	s.mysqlAdvancedNumbers[mysqlKeepAlive] = "0"
	if _, err := s.draftProfile(); err == nil || !strings.Contains(err.Error(), "保持连接间隔") {
		t.Fatalf("invalid keep-alive duration accepted: %v", err)
	}
	s.mysqlAdvancedNumbers[mysqlKeepAlive] = "180"
	adv.Compression = true
	adv.ClientDriver = "旧版"
	adv.Charset = "utf8mb4"
	p, err := s.draftProfile()
	if err != nil {
		t.Fatal(err)
	}
	if !p.MySQLAdvanced.KeepAlive || p.MySQLAdvanced.KeepAliveSeconds != 180 ||
		!p.MySQLAdvanced.Compression || p.MySQLAdvanced.ClientDriver != "旧版" {
		t.Fatalf("advanced draft lost settings: %+v", p.MySQLAdvanced)
	}
	if err := tt.Click("兼容性"); err != nil {
		t.Fatal(err)
	}
	if !s.mysqlCompatibilityVisible || !tt.HasText("使用兼容模式") {
		t.Fatal("compatibility button must reveal original subtab")
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("客户端字符集：") {
		t.Fatal("return from compatibility tab failed")
	}
}

func TestMySQLAdvancedDirectorySelectionGuard(t *testing.T) {
	s := New()
	s.SelectMySQLSettingsDirectory("/tmp/should-not-write")
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.PostgreSQL)
	s.nextConnectionStep()
	s.SelectMySQLSettingsDirectory("/tmp/should-not-write")
	if s.connectionDraft.MySQLAdvanced.SettingsLocation != "" {
		t.Fatal("MySQL settings picker may not alter non-MySQL profile")
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.SelectMySQLSettingsDirectory("/tmp/mysql")
	if s.connectionDraft.MySQLAdvanced.SettingsLocation != "/tmp/mysql" {
		t.Fatal("native directory selection did not update draft")
	}
}

func TestMySQLAdvancedSaveAndEditUI(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "with advanced"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.MySQLAdvanced.Charset = "utf8mb4"
	s.connectionDraft.MySQLAdvanced.Compression = true
	s.mysqlAdvancedNumbers[mysqlConnectTimeout] = "47"
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog || len(s.profileEntries) != 1 {
		t.Fatalf("advanced profile not saved: %q", s.connectionError)
	}
	s.startEditProfile()
	if s.connectionDraft.MySQLAdvanced.Charset != "utf8mb4" ||
		!s.connectionDraft.MySQLAdvanced.Compression ||
		s.mysqlAdvancedNumbers[mysqlConnectTimeout] != "47" {
		t.Fatalf("advanced settings not restored for editing: %+v (%v)",
			s.connectionDraft.MySQLAdvanced, s.mysqlAdvancedNumbers)
	}
	edited := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := edited.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if !edited.HasText("客户端字符集：") {
		t.Fatal("advanced page unavailable when editing persisted profile")
	}
}
