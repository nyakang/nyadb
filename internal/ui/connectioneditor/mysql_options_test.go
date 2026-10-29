package connectioneditor

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLAdvancedUIControlsAndValidation(t *testing.T) {
	s := newTestEditor()
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
	s := newTestEditor()
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

func TestMySQLDatabaseTabNativeEmptyState(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("使用自定义数据库列表") ||
		!tt.HasText("自动打开") ||
		!tt.HasText("添加数据库到列表") ||
		!tt.HasText("从列表中移除数据库") {
		t.Fatal("native Database tab missing observed controls")
	}
	if s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom database list must be off by default")
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList ||
		!tt.HasText("需要建立数据库连接后才能读取列表") {
		t.Fatal("custom list toggle or offline explanation missing")
	}
	// The tester may return nil for a click on a disabled control; verify that
	// nothing was added, no network workflow opened and no state changed.
	_ = tt.Click("添加数据库到列表（需要连接 MySQL）")
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList || s.connectionError != "" {
		t.Fatal("disabled Add must not launch a database operation")
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom list toggle did not return to disabled")
	}
}

func TestMySQLDatabaseTabCustomFlagPersists(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "custom database list"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用自定义数据库列表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("save failed: %s", s.connectionError)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || !list[0].MySQLAdvanced.UseCustomDatabaseList {
		t.Fatalf("custom database-list flag was not persisted: %+v %v", list, err)
	}
	s.startEditProfile()
	if !s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList {
		t.Fatal("custom list flag missing from edit draft")
	}
	edited := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := edited.Click("数据库"); err != nil {
		t.Fatal(err)
	}
	if !edited.HasText("需要建立数据库连接后才能读取列表") {
		t.Fatal("custom list UI not restored on edit")
	}
}

func mysqlEditorState(t *testing.T) (*testEditor, *ui.Tester) {
	t.Helper()
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "mysql-research"
	return s, ui.NewTester(s.ConnectionWindowView, 900, 700)
}

func TestCompatibilityUIEnableAndOverrides(t *testing.T) {
	s, tt := mysqlEditorState(t)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("兼容性"); err != nil {
		t.Fatal(err)
	}
	compat := &s.connectionDraft.MySQLAdvanced.Compatibility
	if compat.Enabled || compat.ForceLowerCase || compat.ForceSQLMode ||
		compat.LowerCaseTableNames != "0" || compat.NDBSupport != "Not Support" ||
		compat.DatabaseMethod != "SHOW DATABASES Statement" ||
		compat.ViewMethod != "SHOW FULL TABLES Statement" {
		t.Fatalf("unexpected defaults: %+v", compat)
	}
	// A disabled child checkbox must not change even when clicked.
	if err := tt.Click("强制 lower_case_table_names 为："); err != nil {
		t.Fatal(err)
	}
	if compat.ForceLowerCase {
		t.Fatal("override changed while compatibility mode was disabled")
	}
	if err := tt.Click("使用兼容模式"); err != nil {
		t.Fatal(err)
	}
	if !compat.Enabled {
		t.Fatal("compatibility mode checkbox did not toggle")
	}
	for _, name := range []string{
		"强制 lower_case_table_names 为：",
		"强制 sql_mode 为：",
		"强制 NDB 集群支持为：",
		"强制数据库列出方法为：",
		"强制视图列出方法为：",
	} {
		if err := tt.Click(name); err != nil {
			t.Fatalf("checkbox %q: %v", name, err)
		}
	}
	if !compat.ForceLowerCase || !compat.ForceSQLMode || !compat.ForceNDB ||
		!compat.ForceDatabaseMethod || !compat.ForceViewMethod {
		t.Fatalf("override toggles not wired: %+v", compat)
	}
	compat.LowerCaseTableNames = "2"
	compat.NDBSupport = "Support"
	compat.DatabaseMethod = "INFORMATION_SCHEMA"
	compat.ViewMethod = "INFORMATION_SCHEMA"
	compat.SQLMode = "STRICT_TRANS_TABLES"
	if err := tt.Click("使用兼容模式"); err != nil {
		t.Fatal(err)
	}
	if compat.Enabled || !compat.ForceLowerCase || !compat.ForceSQLMode {
		t.Fatalf("disabling compatibility must retain per-row choices: %+v", compat)
	}
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLInitialQueryModalCancelConfirmAndClear(t *testing.T) {
	s, tt := mysqlEditorState(t)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("编辑初始查询"); err != nil {
		t.Fatal(err)
	}
	if !s.mysqlInitialQueryOpen || !tt.HasText("复制到剪贴板") {
		t.Fatal("native editor did not open")
	}
	s.mysqlInitialQueryDraft = "SET @secret = 'not-to-be-saved';"
	if err := tt.Click("取消初始查询"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlInitialQueryOpen || s.mysqlInitialQuery != "" || s.mysqlInitialQueryDraft != "" {
		t.Fatal("cancel must discard unconfirmed SQL")
	}
	if err := tt.Click("编辑初始查询"); err != nil {
		t.Fatal(err)
	}
	const sql = "SET ROLE ALL;\nSET @var_name = 'value';"
	s.mysqlInitialQueryDraft = sql
	if err := tt.Click("复制到剪贴板"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != sql {
		t.Fatalf("copy button omitted multiline SQL: %q", tt.Clipboard())
	}
	if err := tt.Click("确认初始查询"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlInitialQueryOpen || s.mysqlInitialQuery != sql ||
		s.mysqlInitialQueryPreview != "SET ROLE ALL; SET @var_name = 'value';" {
		t.Fatalf("confirmed SQL was not preserved in the session: %q %q", s.mysqlInitialQuery, s.mysqlInitialQueryPreview)
	}
	if err := tt.Click("编辑初始查询"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlInitialQueryDraft != sql {
		t.Fatal("reopening must show last confirmed multiline SQL")
	}
	s.mysqlInitialQueryDraft = strings.Repeat("x", 64*1024+1)
	if err := tt.Click("确认初始查询"); err != nil {
		t.Fatal(err)
	}
	if !s.mysqlInitialQueryOpen || s.mysqlInitialQuery != sql || s.mysqlInitialQueryError == "" {
		t.Fatal("oversized SQL should be rejected without discarding confirmed SQL")
	}
	s.cancelMySQLInitialQuery()
	p, err := s.draftProfile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.MySQLAdvanced.Compatibility.SQLMode, "@var_name") {
		t.Fatal("SQL leaked into compatibility settings")
	}
	closed := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := closed.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlInitialQuery != "" || s.mysqlInitialQueryPreview != "" || s.mysqlInitialQueryDraft != "" {
		t.Fatalf("connection close must clear SQL in memory: open=%v modal=%v query=%q preview=%q draft=%q",
			s.connectionDialog, s.mysqlInitialQueryOpen, s.mysqlInitialQuery, s.mysqlInitialQueryPreview, s.mysqlInitialQueryDraft)
	}
}

func TestInitialQueryNeverWrittenToProfiles(t *testing.T) {
	s, _, service, path := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "SQL transient"
	s.beginMySQLInitialQuery()
	const sql = "SET @private_token = 'sensitive-literal-do-not-store';"
	s.mysqlInitialQueryDraft = sql
	s.confirmMySQLInitialQuery()
	s.saveConnectionDraft()
	if s.connectionDialog || s.mysqlInitialQuery != "" {
		t.Fatalf("save should close and clear transient SQL: %q", s.connectionError)
	}
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 {
		t.Fatalf("saved profiles: %+v %v", profiles, err)
	}
	if strings.Contains(profiles[0].MySQLAdvanced.Compatibility.SQLMode, sql) {
		t.Fatal("initial SQL leaked into the saved profile")
	}
	// Check the on-disk database bytes directly; SQL cannot be present in any field.
	raw, err := readProfileFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive-literal-do-not-store") {
		t.Fatal("initial SQL persisted to local SQLite")
	}
}

func readProfileFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
