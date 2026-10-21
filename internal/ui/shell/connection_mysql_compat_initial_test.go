package shell

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlEditorState(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s := New()
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
