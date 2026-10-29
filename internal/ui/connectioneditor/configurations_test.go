package connectioneditor

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func editMySQLProfileTester(t *testing.T) (*testEditor, *ui.Tester, *connections.Service, int64) {
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

func TestMySQLEditorAlternativeContextActions(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("配置文件名称"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("Staging")
	if s.connectionOtherConfigs[0].name != "Staging" {
		t.Fatalf("native alternative name edit failed: %q", s.connectionOtherConfigs[0].name)
	}
	if err := tt.RightClick("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制配置文件"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionOtherConfigs) != 2 || s.connectionOtherConfigs[1].name != "Staging 副本" {
		t.Fatalf("context copy failed: %+v", s.connectionOtherConfigs)
	}
	if err := tt.RightClick("切换配置文件：Staging 副本"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除配置文件"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionOtherConfigs) != 1 || s.connectionActiveConfig != 0 {
		t.Fatalf("context delete failed: %+v selected=%d", s.connectionOtherConfigs, s.connectionActiveConfig)
	}
	// The changes above are drafts until the user explicitly confirms.
	got, err := service.Get(context.Background(), id)
	if err != nil || len(got.Variants) != 0 {
		t.Fatal("context action persisted before Confirm")
	}
	s.saveConnectionDraft()
	got, err = service.Get(context.Background(), id)
	if err != nil || len(got.Variants) != 1 || got.Variants[0].Name != "Staging" {
		t.Fatalf("confirmed context operations were not saved: %+v %v", got.Variants, err)
	}
	if got.Kind != connections.MySQL {
		t.Fatal("context actions changed parent type")
	}
}

func TestMySQLConfigurationSwitchRetainsCommonTab(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 {
		t.Fatal("Advanced tab was not selected")
	}
	if err := tt.Click("切换配置文件：主要配置文件 (Active)"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("设置位置：") {
		t.Fatal("switching to main configuration changed the active tab")
	}
	if err := tt.Click("切换配置文件：New Profile"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditorTab != 1 || !tt.HasText("设置位置：") {
		t.Fatal("switching to alternative configuration changed the active tab")
	}
}

func TestMySQLActiveConfigurationIsIndependentOfSelection(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	s.connectionOtherConfigs[0].name = "Staging"
	s.connectionDraft.Host = "staging.example.test"
	s.connectionPort = "3307"
	tt.Frame()
	if err := tt.RightClick("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("设置为活动配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveTarget != 1 || !tt.HasText("Staging (Active)") ||
		tt.HasText("主要配置文件 (Active)") {
		t.Fatal("Active marker did not move to staging")
	}
	if err := tt.Click("切换配置文件：主要配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveConfig != 0 || s.connectionActiveTarget != 1 {
		t.Fatal("highlighting main unexpectedly changed Active target")
	}
	got, err := service.ActiveSettings(context.Background(), id)
	if err != nil || got.Host != "localhost" {
		t.Fatal("draft Active state leaked to storage")
	}
	s.closeConnectionDialog()
	s.startEditProfile()
	tt.Frame()
	if !tt.HasText("主要配置文件 (Active)") || len(s.connectionOtherConfigs) != 0 {
		t.Fatal("cancel failed to discard draft Active state")
	}
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	s.connectionOtherConfigs[0].name = "Replica"
	s.connectionDraft.Host = "replica.example.test"
	tt.Frame()
	if err := tt.RightClick("切换配置文件：Replica"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("设置为活动配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("Active selection not saved: %s", s.connectionError)
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "Replica" {
		t.Fatalf("Active save: %+v %v", p, err)
	}
	resolved, err := service.ActiveSettings(context.Background(), id)
	if err != nil || resolved.Host != "replica.example.test" ||
		resolved.ConfigurationName != "Replica" {
		t.Fatalf("resolver mismatch: %+v %v", resolved, err)
	}
	s.startEditProfile()
	tt.Frame()
	if s.connectionActiveTarget != 1 || !tt.HasText("Replica (Active)") {
		t.Fatal("saved Active selection not restored on reopen")
	}
	// Selection and Active stay independent across keyboard-accessible buttons.
	if err := tt.Click("切换配置文件：主要配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveTarget != 1 || s.connectionActiveConfig != 0 {
		t.Fatal("selection changed the persisted Active target")
	}
}

func TestMySQLActiveRenameDuplicateAndDelete(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	s.connectionOtherConfigs[0].name = "A"
	s.markConnectionConfigurationActive(1)
	s.connectionOtherConfigs[0].name = "Renamed"
	s.duplicateConnectionConfiguration(1)
	if s.connectionActiveTarget != 1 || len(s.connectionOtherConfigs) != 2 {
		t.Fatal("duplicating a configuration changed Active selection")
	}
	s.removeConnectionConfiguration(2)
	if s.connectionActiveTarget != 1 {
		t.Fatal("deleting a non-active configuration changed Active")
	}
	s.addConnectionConfiguration()
	s.removeConnectionConfiguration(1)
	if s.connectionActiveTarget != 0 {
		t.Fatal("deleting the Active configuration did not select primary")
	}
	tt.Frame()
	if !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("primary was not relabeled Active")
	}
	s.saveConnectionDraft()
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "" || len(p.Variants) != 1 {
		t.Fatalf("delete did not persist fallback: %+v %v", p, err)
	}
	// Recreate one named Active and verify rename propagates on confirmation.
	s.startEditProfile()
	s.markConnectionConfigurationActive(1)
	s.connectionOtherConfigs[0].name = "Renamed-again"
	s.saveConnectionDraft()
	p, err = service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "Renamed-again" ||
		!strings.EqualFold(p.Variants[0].Name, p.ActiveVariant) {
		t.Fatalf("renaming an Active configuration left a dangling reference: %+v %v", p, err)
	}
}

func TestMySQLActiveInvalidSaveIsAtomic(t *testing.T) {
	s, _, service, id := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	s.markConnectionConfigurationActive(1)
	s.connectionPort = "invalid"
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("invalid Active config closed window")
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "" || len(p.Variants) != 0 {
		t.Fatalf("failed save modified persisted selection: %+v %v", p, err)
	}
	s.ConnectionWindowClosed()
	got, err := service.ActiveSettings(context.Background(), id)
	if err != nil || got.ConfigurationName != "" {
		t.Fatalf("window close changed Active: %+v %v", got, err)
	}
}

// Buttons in the configuration sidebar are native MyGo controls, rather than
// pointer-only hit targets. Arrow-key navigation changes only the
// highlighted editor row; it must never silently change the Active target.
func TestMySQLConfigurationSidebarKeyboardSelection(t *testing.T) {
	s, tt, _, _ := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	tt.Frame()
	if err := tt.Click("切换配置文件：主要配置文件 (Active)"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveConfig != 0 {
		t.Fatal("primary not selected")
	}
	tt.Key(0, ui.KeyDown)
	if s.connectionActiveConfig != 1 || s.connectionActiveTarget != 0 {
		t.Fatalf("keyboard selection incorrectly changed Active or failed to navigate: selected=%d active=%d", s.connectionActiveConfig, s.connectionActiveTarget)
	}
	if err := tt.Click("切换配置文件：New Profile"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyUp)
	if s.connectionActiveConfig != 0 || s.connectionActiveTarget != 0 {
		t.Fatal("Up arrow did not restore primary without changing Active")
	}
}
