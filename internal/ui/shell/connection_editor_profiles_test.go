package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func editMySQLProfileTester(t *testing.T) (*State, *ui.Tester, *connections.Service, int64) {
	t.Helper()
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Editing", Kind: connections.MySQL, Host: "localhost", Port: 3306, Username: "root",
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	s.SetConnectionWindowHost(func() {}, func() {})
	s.startEditProfile()
	return s, ui.NewTester(s.ConnectionWindowView, 900, 700), service, p.ID
}

func TestMySQLEditorSidebarAndWindowLifecycle(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if s.ConnectionWindowTitle() != "Editing - 编辑连接 (MySQL)" ||
		!tt.HasText("连接配置文件") || !tt.HasText("主要配置文件 (Active)") ||
		tt.HasText("上一步") {
		t.Fatalf("wrong edit window state: title=%s visible=%v", s.ConnectionWindowTitle(), tt.Texts())
	}
	if err := tt.Click("切换连接配置文件侧栏"); err != nil {
		t.Fatal(err)
	}
	if s.connectionProfilesVisible || tt.HasText("主要配置文件 (Active)") {
		t.Fatal("footer did not collapse sidebar")
	}
	if err := tt.Click("切换连接配置文件侧栏"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionProfilesVisible || !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("footer did not restore sidebar")
	}
	s.connectionDraft.Host = "cancelled.example.test"
	s.closeConnectionDialog()
	got, err := service.Get(context.Background(), id)
	if err != nil || got.Host != "localhost" {
		t.Fatalf("cancel persisted draft: %+v (%v)", got, err)
	}
	s.startEditProfile()
	if s.connectionDraft.Host != "localhost" || !s.connectionProfilesVisible || len(s.connectionOtherConfigs) != 0 {
		t.Fatal("reopening editor retained stale draft or sidebar state")
	}
	s.connectionDraft.Host = "os-close.example.test"
	s.connectionSecret = "temporary-secret"
	s.ConnectionWindowClosed()
	if s.connectionDialog || s.connectionSecret != "" || s.connectionProfilesVisible {
		t.Fatal("OS close retained editor state or credentials")
	}
	got, err = service.Get(context.Background(), id)
	if err != nil || got.Host != "localhost" {
		t.Fatal("OS close persisted an unsaved change")
	}
	// New connection continues to use the chooser, without an edit sidebar.
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt.Frame()
	if tt.HasText("主要配置文件 (Active)") || s.connectionDraft.ID != 0 ||
		!tt.HasText("上一步") || s.ConnectionWindowTitle() != "新建连接 (MySQL)" {
		t.Fatalf("edit state leaked into new connection: %+v", s.connectionDraft)
	}
}

func TestMySQLEditorAlternativeSwitchSaveCancelReopen(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionOtherConfigs) != 1 || s.connectionActiveConfig != 1 ||
		!tt.HasText("New Profile") || !tt.HasText("配置文件名称：") {
		t.Fatalf("new alternative not selected: %+v", s.connectionOtherConfigs)
	}
	s.connectionOtherConfigs[0].name = "Staging"
	s.connectionDraft.Host = "staging.example.test"
	s.connectionPort = "3307"
	s.mysqlAdvancedNumbers[mysqlConnectTimeout] = "45"
	s.connectionSecret = "unpersisted-db-password"
	s.mysqlSSHPassword = "unpersisted-ssh-password"
	s.mysqlHTTPPassword = "unpersisted-http-password"
	if err := tt.Click("切换配置文件：主要配置文件 (Active)"); err != nil {
		t.Fatal(err)
	}
	if s.connectionSecret != "" || s.mysqlSSHPassword != "" || s.mysqlHTTPPassword != "" {
		t.Fatal("switching configurations retained session credentials")
	}
	if s.connectionActiveConfig != 0 || s.connectionDraft.Host != "localhost" {
		t.Fatalf("main config not restored: %+v", s.connectionDraft)
	}
	s.connectionDraft.Host = "primary.example.test"
	if err := tt.Click("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Host != "staging.example.test" || s.connectionPort != "3307" ||
		s.mysqlAdvancedNumbers[mysqlConnectTimeout] != "45" {
		t.Fatalf("alternative lost draft fields: %+v port=%q numbers=%v", s.connectionDraft, s.connectionPort, s.mysqlAdvancedNumbers)
	}
	// Name belongs to the parent connection: the alternative must not rename it.
	if r, ok := tt.Find("连接名称"); !ok || r.W == 0 {
		t.Fatal("alternative lacks disabled parent-name field")
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("saving alternatives failed: %s", s.connectionError)
	}
	got, err := service.Get(context.Background(), id)
	if err != nil || got.Host != "primary.example.test" || len(got.Variants) != 1 ||
		got.Variants[0].Name != "Staging" || got.Variants[0].Port != 3307 ||
		got.Variants[0].MySQLAdvanced.ConnectTimeoutSeconds != 45 {
		t.Fatalf("atomic profile save lost configurations: %+v (%v)", got, err)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal("alternative incorrectly became a root connection")
	}
	s.startEditProfile()
	tt.Frame()
	if !tt.HasText("Staging") || s.connectionActiveConfig != 0 ||
		!s.connectionProfilesVisible {
		t.Fatal("reopen did not restore saved alternatives / active main")
	}
	if err := tt.Click("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Host != "staging.example.test" {
		t.Fatal("reopened alternative has wrong host")
	}
	s.connectionDraft.Host = "discard.example.test"
	s.connectionOtherConfigs[0].name = "Discarded"
	s.closeConnectionDialog()
	s.startEditProfile()
	tt.Frame()
	if !tt.HasText("Staging") || tt.HasText("Discarded") {
		t.Fatal("cancel unexpectedly saved alternative changes")
	}
	got, _ = service.Get(context.Background(), id)
	if got.Variants[0].Host != "staging.example.test" {
		t.Fatal("cancel overwrote variant")
	}
}

func TestMySQLEditorInvalidAlternativeKeepsWindow(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	s.connectionPort = "oops"
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDialog || s.connectionError == "" ||
		!strings.Contains(s.connectionError, "配置文件") {
		t.Fatalf("invalid alternative saved or not reported: %q", s.connectionError)
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || len(p.Variants) != 0 {
		t.Fatalf("invalid alternative persisted: %+v %v", p.Variants, err)
	}
}
