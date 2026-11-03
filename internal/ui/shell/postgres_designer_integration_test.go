package shell

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func TestPostgreSQLDesignShellFixture(t *testing.T) {
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires explicit disposable PostgreSQL fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("missing fixture port")
	}

	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "PostgreSQL designer fixture", Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	waitPostgresDesigner(t, tt, func() bool { return s.sessions.Current().Status != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	s.openMySQLDatabase(services, "public", true)
	waitPostgresDesigner(t, tt, func() bool { _, loaded := w.Cache["public"]; return w.Pending == "" && loaded })
	if s.objectWorkspace.CanAccounts(p.ID) || s.objectWorkspace.CanChangeTable(p.ID) {
		t.Fatal("table designer enabled unrelated administration")
	}
	if !s.objectWorkspace.CanCreateTable(p.ID, "public") {
		t.Fatal("PostgreSQL feature binding did not enable new table")
	}
	if err := tt.Click("新建表"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("新表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("designed_table")
	if err := tt.Click("确认新建表"); err != nil {
		t.Fatal(err)
	}
	doc := s.currentTab().object
	if doc == nil {
		t.Fatal("new table did not open a structure document")
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.DesignBusy() && tt.HasText("添加字段") })
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("字段名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("payload")
	if err := tt.Click("应用定义修改"); err != nil {
		t.Fatal(err)
	}
	tt.Key(primaryModifier(), ui.KeyS)
	if !tt.HasText("确认执行结构 SQL") {
		t.Fatal("save shortcut did not open the designer preview")
	}
	if doc.DesignBusy() {
		t.Fatal("preview executed SQL")
	}
	if err := tt.Click("确认执行结构 SQL"); err != nil {
		t.Fatal(err)
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.DesignBusy() })
	if doc.Error() != "" || doc.DesignDirty() {
		t.Fatalf("create receipt: %s dirty=%v", doc.Error(), doc.DesignDirty())
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.Loading() })
	if doc.Data() == nil || len(doc.Data().Columns) != 2 {
		t.Fatalf("create inspector did not refresh: %+v %s", doc.Data(), doc.Error())
	}
	metadata, err := s.runtime.Metadata(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	o := s.runtime.Namespace(p.ID, "public").Object("table", "designed_table")
	structure, err := metadata.TableMetadata(context.Background(), o)
	if err != nil || len(structure.Columns) != 2 {
		t.Fatalf("actual created table: %+v %v", structure, err)
	}
	// Seed typed data through the real session, then add a field with the UI.
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := session.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Execute(context.Background(), "INSERT INTO public.designed_table(id,payload) VALUES(3,'retained')"); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("字段名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("added")
	if err := tt.Click("应用定义修改"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认执行结构 SQL"); err != nil {
		t.Fatal(err)
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.DesignBusy() })
	if doc.Error() != "" {
		t.Fatal(doc.Error())
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.Loading() })
	if doc.Data() == nil || len(doc.Data().Columns) != 3 || doc.Data().Columns[2].Name != "added" {
		t.Fatalf("rebuild inspector retained old fields: %+v %s", doc.Data(), doc.Error())
	}
	page, err := session.Page(context.Background(), contract.PageRequest{Object: o, Limit: 10})
	if err != nil || len(page.Rows) != 1 || len(page.Rows[0]) != 3 || page.Rows[0][1].Text != "retained" || page.Rows[0][2].Kind != contract.Null {
		t.Fatalf("actual rebuild: %+v %v", page, err)
	}
	// F2/right-click wiring delegates to this same rename callback. Opening it
	// does not execute; the native preview still needs explicit confirmation.
	s.objectWorkspace.BeginSchemaRename(doc, "renamed_table", services)
	tt.Frame()
	if doc.Object() != o {
		t.Fatal("rename draft changed identity before confirmation")
	}
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认执行结构 SQL"); err != nil {
		t.Fatal(err)
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.DesignBusy() })
	if doc.Error() != "" || doc.Table() != "renamed_table" || s.currentTab().title != "public.renamed_table" {
		t.Fatalf("rename scope/title: %s %s %s", doc.Error(), doc.Table(), s.currentTab().title)
	}
	waitPostgresDesigner(t, tt, func() bool { return !doc.Loading() })
	if doc.Data() == nil || len(doc.Data().Columns) != 3 || doc.Error() != "" {
		t.Fatalf("rename inspector used old table identity: %+v %s", doc.Data(), doc.Error())
	}
	if _, err = metadata.TableMetadata(context.Background(), o); err == nil {
		t.Fatal("old table still exists")
	}
	o.Name = "renamed_table"
	page, err = session.Page(context.Background(), contract.PageRequest{Object: o, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "retained" {
		t.Fatalf("renamed data: %+v %v", page, err)
	}
	tt.SetSize(840, 560)
	tt.Frame()
	if !tt.HasText("SQL 预览") || !tt.HasText("表持久性") {
		t.Fatal("narrow formal window lost designer controls")
	}
	// An uncertain receipt revokes only the captured handle. On the next UI
	// frame sessionbrowser detects its loss, while the leave guard retains
	// the unapplied design instead of disposing it during disconnection.
	tt.SetSize(1280, 800)
	s.objectWorkspace.BeginSchemaRename(doc, "unapplied_name", services)
	tt.Frame()
	target := contract.SchemaDesignTarget{Object: o, Generation: s.runtime.Generation(p.ID)}
	s.schemaTableDesigned(services, objectworkspace.SchemaDesignEvent{DocumentID: s.currentTab().id, Original: target, Generation: w.Generation, Database: "public", Outcome: contract.SchemaDesignOutcome{State: "committed", Verified: true}})
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || s.sessions.StatusFor(p.ID) == "已连接" {
		t.Fatal("uncertain receipt left a connected session visible")
	}
	if !doc.DesignDirty() || !tt.HasText("表设计有未保存的修改") {
		t.Fatal("lost session disposed the unapplied designer draft")
	}
}

func waitPostgresDesigner(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if !done() {
		t.Fatal("PostgreSQL designer did not settle")
	}
	tt.Frame()
}

func TestPostgreSQLDesignReceiptReplacementFixture(t *testing.T) {
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires explicit disposable PostgreSQL fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("missing fixture port")
	}
	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "PostgreSQL receipt fixture", Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.runtime.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(p.ID, "public").Object("table", "original"), Generation: original.Generation()}
	replacement, err := s.runtime.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() == target.Generation {
		t.Fatal("replacement reused the public generation")
	}
	s.schemaTableDesigned(ui.Services{}, objectworkspace.SchemaDesignEvent{Original: target, Generation: 9, Database: "public", Outcome: contract.SchemaDesignOutcome{State: "committed", Verified: true}})
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("invalid old commit closed the replacement")
	}
	s.schemaTableDesigned(ui.Services{}, objectworkspace.SchemaDesignEvent{Original: target, Generation: 9, Database: "public", Outcome: contract.SchemaDesignOutcome{State: "uncertain"}})
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("uncertain old commit closed the replacement")
	}
}
