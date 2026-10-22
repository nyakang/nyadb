package shell

import (
	"context"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt-in integration: a real managed session and real MySQL 8.4 metadata are
// rendered by MyGo's native Table/Tabs in the headless tester. This does not
// claim that the physical desktop or Navicat have been differential-verified.
func TestMySQLStructureRealDatabaseNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires named disposable MySQL 8.4 container, localhost port 34306")
	}
	password := mysqlIntegrationPassword(t)
	s, _, service, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "NyaDB localhost fixture", Kind: connections.MySQL,
		Host: "127.0.0.1", Port: 34306, Username: "nyadb_it_reader",
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	settings, err := connections.ResolveActiveSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.mysqlSessionManager.Connect(ctx, settings, password); err != nil {
		t.Fatalf("isolated session: %v", err)
	}
	s.mysqlSessionProfileID = p.ID
	s.mysqlSessionStatus = "已连接"
	s.mysqlSessionDatabases, err = s.mysqlSessionManager.Databases(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.mysqlSessionDatabases, "nyadb_struct_a") {
		t.Fatalf("fixture not visible: %v", s.mysqlSessionDatabases)
	}

	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("浏览数据库：nyadb_struct_a"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	objects := s.mysqlSelectedObjects()
	if len(objects) != 3 || objects[0].Name != "child" {
		t.Fatalf("real metadata object list not rendered: %+v", objects)
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	str := s.currentTab().structure
	if str == nil || str.Name != "child" || str.Database != "nyadb_struct_a" {
		t.Fatalf("viewer did not use real database metadata: %+v", str)
	}
	if !tt.HasText("nyadb_struct_a / child") || !tt.HasText("id") ||
		!tt.HasText("price") || !tt.HasText("nullable_note") {
		t.Fatalf("real columns not visible in MyGo native table: %v", tt.Texts())
	}
	if path := os.Getenv("NYADB_MYSQL_REAL_VIEWER_CAPTURE"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("索引"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("uk_tenant_code") || !tt.HasText("ix_note_prefix") {
		t.Fatalf("real indexes not visible: %v", tt.Texts())
	}
	if err := tt.Click("外键"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("fk_child_parent") || !tt.HasText("CASCADE") {
		t.Fatalf("real foreign key not visible: %v", tt.Texts())
	}
	if err := tt.Click("刷新表结构：child"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().structure == nil || len(s.currentTab().structure.Columns) != 8 {
		t.Fatal("refresh failed to reload real MySQL structure")
	}
	// Opening the identically named object in a second real database must not
	// reuse or overwrite the first database's cached metadata.
	first := s.currentTab()
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("切换数据库：nyadb_struct_b"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	second := s.currentTab()
	if second == first || second.title != "nyadb_struct_b.child" ||
		second.structure == nil || len(second.structure.Columns) != 2 ||
		len(s.tabs) != 3 {
		t.Fatalf("cross-database structure tabs were mixed: %+v", s.tabs)
	}
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("切换数据库：nyadb_struct_a"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	s.objectRow = 0
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab() != first || len(s.tabs) != 3 ||
		s.currentTab().structure == nil || len(s.currentTab().structure.Columns) != 8 {
		t.Fatal("reopening first schema should focus its cached tab")
	}
	// A view is a distinct read-only structure workspace, not static table rows.
	if err := tt.Click("返回对象列表"); err != nil {
		t.Fatal(err)
	}
	s.objectRow = 2 // child, parent, parent_view in the server-ordered object list
	tt.Frame()
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if s.currentTab().title != "nyadb_struct_a.parent_view" ||
		s.currentTab().structure.Kind != "VIEW" ||
		len(s.currentTab().structure.Columns) != 3 || len(s.tabs) != 4 {
		t.Fatalf("view structure was not rendered: %+v", s.currentTab().structure)
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 || s.mysqlSessionStatus != "未连接" {
		t.Fatal("disconnect did not discard real cached structure tab")
	}
}

// mysqlIntegrationPassword accepts an explicitly supplied password or a private
// file path. The file option keeps credentials out of commands and test logs.
// Both callers require NYADB_MYSQL_STRUCTURE_IT=1 before reaching this helper.
func mysqlIntegrationPassword(t *testing.T) string {
	t.Helper()
	value := os.Getenv("NYADB_MYSQL_IT_PASSWORD")
	if value != "" {
		return value
	}
	file := os.Getenv("NYADB_MYSQL_IT_PASSWORD_FILE")
	if file == "" {
		t.Fatal("explicit test credential is required via NYADB_MYSQL_IT_PASSWORD or NYADB_MYSQL_IT_PASSWORD_FILE")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read isolated test credential: %v", err)
	}
	value = strings.TrimSpace(string(data))
	if value == "" {
		t.Fatal("isolated test credential is empty")
	}
	return value
}
