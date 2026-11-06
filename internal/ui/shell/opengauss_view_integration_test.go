package shell

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestOpenGaussViewNativeMenusPreviewAndTabLifecycleFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires disposable openGauss fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENGAUSS_PORT"), 10, 16)
	if err != nil || port < 1024 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name, p.Host, p.Port, p.Database, p.Username = "openGauss view management", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
	p.TLSMode, p.PostgreSQLOptions.CAFile = "disable", os.Getenv("NYADB_OPENGAUSS_CA_FILE")
	if p.PostgreSQLOptions.CAFile != "" {
		p.TLSMode = "verify-full"
	}
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = os.Getenv("NYADB_OPENGAUSS_PASSWORD")
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Status != "连接中" })
	if s.sessions.Current().Status != "已连接" {
		t.Fatal("native view session", s.sessions.Current().Error)
	}
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Rendering the complete UI workflow under race instrumentation can take
	// over a minute; individual asynchronous transitions remain separately bounded.
	ctx, stop := context.WithTimeout(context.Background(), 180*time.Second)
	defer stop()
	lease, err := session.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	schema := fmt.Sprintf("nyadb_gauss_ui_views_%d", time.Now().UnixNano())
	quotedSchema := `"` + schema + `"`
	if _, err := lease.Execute(ctx, "CREATE SCHEMA "+quotedSchema+"; CREATE TABLE "+quotedSchema+".source(id BIGINT PRIMARY KEY,note TEXT); INSERT INTO "+quotedSchema+".source VALUES(1,'原始 alpha'); CREATE TABLE "+quotedSchema+".other(id BIGINT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := lease.Execute(cleanup, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	// Refresh the native namespace list after this test's schema is created.
	s.sessions.RefreshDatabases(services.Invalidate)
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, schema))
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[schema] != nil })
	shellCreateView(t, s, tt, "created_view", "CREATE VIEW "+quotedSchema+".created_view(id,note) WITH(security_barrier=true) AS SELECT id,note FROM source WITH LOCAL CHECK OPTION;")
	target := s.runtime.Namespace(p.ID, schema).Object("view", "created_view")
	if _, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("native create preview executed SQL")
	}
	if output := os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "opengauss-live-view-preview", tt)
	}
	shellConfirmView(t, s, tt)
	menu := func(name, item string) {
		t.Helper()
		s.activeTab = 0
		s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Category: "视图", Object: name})
		tt.Frame()
		if err := tt.RightClick(name); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(item); err != nil {
			t.Fatal(err)
		}
		waitSQLiteView(t, s, tt)
	}
	s.openMySQLData(services, schema, "created_view")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.openMySQLStructure(services, schema, "created_view")
	o := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
	s.openMySQLData(services, schema, "other")
	other := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !other.Loading() })
	menu("created_view", "编辑完整定义…")
	if err := tt.Click("完整 CREATE VIEW 定义"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	editorText := tt.Clipboard()
	if !strings.Contains(editorText, "security_barrier=true") || !strings.Contains(editorText, "check_option=local") {
		t.Fatal("native editor lost stored view flags")
	}
	setTestInput(t, tt, "完整 CREATE VIEW 定义", "CREATE VIEW "+quotedSchema+".created_view(id,note) WITH(security_barrier=true,check_option=local) AS SELECT id,upper(note) FROM source")
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	before, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(before.Rows) != 1 || before.Rows[0][1].Text != "原始 alpha" {
		t.Fatal("native edit preview mutated view", err)
	}
	shellConfirmView(t, s, tt)
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() && !o.Loading() })
	if d.Page() == nil || len(d.Page().Rows) != 1 || d.Page().Rows[0][1].Text != "原始 ALPHA" {
		t.Fatal("native edit did not refresh original view data", d.Error())
	}
	menu("created_view", "重命名视图…")
	setTestInput(t, tt, "视图名称", "renamed_view")
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	shellConfirmView(t, s, tt)
	foundOther := false
	for _, tab := range s.tabs {
		if tab.data == d || tab.object == o {
			t.Fatal("native rename retained original scoped tabs")
		}
		foundOther = foundOther || tab.data == other
	}
	if !foundOther {
		t.Fatal("native rename closed unrelated table")
	}
	target.Name = "renamed_view"
	s.openMySQLData(services, schema, "renamed_view")
	renamed := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !renamed.Loading() })
	if output := os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "opengauss-live-view-renamed", tt)
	}
	s.openQuery()
	q := s.currentTab().query
	q.Editor.Buffer.Set("SELECT unsaved_openGauss_text")
	menu("renamed_view", "删除对象…")
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	setTestInput(t, tt, "确认原视图名称", "wrong")
	if err := tt.Click("确认执行视图 SQL"); err == nil && s.objectWorkspace.ViewPending() {
		t.Fatal("wrong name dispatched native deletion")
	}
	setTestInput(t, tt, "确认原视图名称", "renamed_view")
	shellConfirmView(t, s, tt)
	if _, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("native drop did not remove view")
	}
	for _, tab := range s.tabs {
		if tab.data == renamed {
			t.Fatal("native drop retained removed view data")
		}
	}
	if q.Editor.Buffer.String() != "SELECT unsaved_openGauss_text" {
		t.Fatal("native drop changed unsaved SQL")
	}
	target.Kind, target.Name = "table", "source"
	page, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "原始 alpha" {
		t.Fatal("view management modified source data", err)
	}
	if strings.Contains(s.objectWorkspace.ViewError(), "sqlite") {
		t.Fatal("native view editor used SQLite dialect")
	}
}
