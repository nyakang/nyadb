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

func TestOpenGaussTableDropMenuPreviewAndOriginalTabCleanupFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires disposable openGauss fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENGAUSS_PORT"), 10, 16)
	if err != nil || port < 1024 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name, p.Host, p.Port, p.Database, p.Username = "openGauss table deletion", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
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
		t.Fatal(s.sessions.Current().Error)
	}
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	lease, err := session.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	schema := fmt.Sprintf("nyadb_gauss_ui_drop_%d", time.Now().UnixNano())
	qualified := `"` + schema + `"`
	if _, err := lease.Execute(ctx, "CREATE SCHEMA "+qualified+"; CREATE TABLE "+qualified+".original(id BIGINT PRIMARY KEY,note TEXT); INSERT INTO "+qualified+".original VALUES(1,'unchanged before confirmation'); CREATE TABLE "+qualified+".other(id bigint)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := lease.Execute(cleanup, "DROP SCHEMA "+qualified+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	s.sessions.RefreshDatabases(services.Invalidate)
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, schema))
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[schema] != nil })
	if !s.objectWorkspace.CanDropTable(p.ID, schema, "original") || !s.objectWorkspace.CanCopyTable(p.ID, schema, "original") {
		t.Fatal("native table menu availability")
	}
	s.openMySQLData(services, schema, "original")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.openMySQLStructure(services, schema, "original")
	o := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
	s.openMySQLData(services, schema, "other")
	other := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !other.Loading() })
	s.openQuery()
	q := s.currentTab().query
	q.Editor.Buffer.Set("SELECT unsaved_native_table_sql")
	s.activeTab = 0
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Category: "表", Object: "original"})
	tt.Frame()
	if err := tt.RightClick("original"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除表"); err != nil {
		t.Fatal(err)
	}
	shellDropPreview(t, tt)
	waitTableChange(t, s, tt)
	if s.objectWorkspace.TableError() != "" {
		t.Fatal(s.objectWorkspace.TableError())
	}
	if err := tt.Click("删除 SQL 预览"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if !strings.Contains(tt.Clipboard(), qualified+`."original" RESTRICT;`) {
		t.Fatal("native deletion preview lost qualified RESTRICT")
	}
	target := s.runtime.Namespace(p.ID, schema).Object("table", "original")
	page, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "unchanged before confirmation" {
		t.Fatal("preview executed native deletion", err)
	}
	if output := os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"); output != "" {
		setTestInput(t, tt, "确认原表名称", "original")
		writeDomainCapture(t, output, "opengauss-live-table-drop-preview", tt)
	}
	tt.SetSize(840, 560)
	tt.Frame()
	setTestInput(t, tt, "确认原表名称", "wrong")
	if err := tt.Click("确认删除表"); err == nil && s.objectWorkspace.TablePending() {
		t.Fatal("incorrect native name dispatched deletion")
	}
	setTestInput(t, tt, "确认原表名称", "original")
	if err := tt.Click("确认删除表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
		t.Fatal("native table deletion did not settle", s.objectWorkspace.TableError())
	}
	if _, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("confirmed native table remains")
	}
	foundOther := false
	for _, tab := range s.tabs {
		if tab.data == d || tab.object == o {
			t.Fatal("native deletion retained original scoped document")
		}
		foundOther = foundOther || tab.data == other
	}
	if !foundOther || q.Editor.Buffer.String() != "SELECT unsaved_native_table_sql" {
		t.Fatal("native deletion changed unrelated data/SQL document")
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	for _, item := range s.sessions.Current().Cache[schema] {
		if item.Name == "original" {
			t.Fatal("native deletion retained original navigator metadata")
		}
	}
}
