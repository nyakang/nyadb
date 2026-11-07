package shell

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestOpenGaussTableRenamePreviewDraftDecisionAndOriginalTabCleanupFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires disposable openGauss fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENGAUSS_PORT"), 10, 16)
	if err != nil || port < 1024 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name, p.Host, p.Port, p.Database, p.Username = "openGauss table rename", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
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
	schema := fmt.Sprintf("nyadb_gauss_ui_rename_%d", time.Now().UnixNano())
	ns := `"` + schema + `"`
	if _, err := lease.Execute(ctx, "CREATE SCHEMA "+ns+"; CREATE TABLE "+ns+".original(id BIGSERIAL PRIMARY KEY,note TEXT,amount NUMERIC(40,20)); INSERT INTO "+ns+".original(note,amount) VALUES('source preserved',9007199254740993.12345678901234567890)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := lease.Execute(cleanup, "DROP SCHEMA "+ns+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	s.sessions.RefreshDatabases(services.Invalidate)
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, schema))
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[schema] != nil })
	s.openMySQLData(services, schema, "original")
	dataDoc := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !dataDoc.Loading() })
	s.dataWorkspace.EditCell(services, dataDoc, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved before rename"})
	if !dataDoc.HasDraft() {
		t.Fatal("cannot stage original draft")
	}
	s.openMySQLStructure(services, schema, "original")
	objectDoc := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !objectDoc.Loading() })
	s.openQuery()
	query := s.currentTab().query
	query.Editor.Buffer.Set("SELECT unsaved_original_rename_sql")
	s.activeTab = 0
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Category: "表", Object: "original"})
	tt.Frame()
	if err := tt.RightClick("original"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatal(err)
	}
	shellDropPreview(t, tt)
	waitTableChange(t, s, tt)
	destination := `中文"目标`
	setTestInput(t, tt, "目标表名称", destination)
	if s.objectWorkspace.TableError() != "" {
		t.Fatal(s.objectWorkspace.TableError())
	}
	if err := tt.Click("重命名 SQL 预览"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if !strings.Contains(tt.Clipboard(), ns+`."original" RENAME TO `+schemaddl.Quote(destination)+";") {
		t.Fatal("qualified rename preview missing", tt.Clipboard())
	}
	target := s.runtime.Namespace(p.ID, schema).Object("table", destination)
	if _, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10}); err == nil {
		t.Fatal("preview renamed original")
	}
	if output := os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{840, 560}, {530, 430}} {
				tt.SetSize(size[0], size[1])
				tt.Frame()
				writeDomainCapture(t, output, fmt.Sprintf("opengauss-live-table-rename-preview-%s-%dx%d", language, size[0], size[1]), tt)
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	tt.SetSize(840, 560)
	tt.Frame()
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !dataDoc.HasDraft() || s.objectWorkspace.TablePending() {
		t.Fatal("rename bypassed draft decision")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !dataDoc.HasDraft() || !s.objectWorkspace.TableDialogOpen() {
		t.Fatal("cancel lost original draft or preview")
	}
	if err := tt.Click("确认重命名表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("应用更改"); err != nil {
		t.Fatal(err)
	}
	waitSQLiteDesigner(t, tt, func() bool { return !dataDoc.HasDraft() && !s.objectWorkspace.TablePending() })
	if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
		t.Fatal("native rename failed", s.objectWorkspace.TableError())
	}
	page, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "saved before rename" || page.Rows[0][2].Text != "9007199254740993.12345678901234567890" {
		t.Fatal("rename lost saved original values", page, err)
	}
	original := target
	original.Name = "original"
	if _, err := session.Page(ctx, contract.PageRequest{Object: original, Limit: 10}); err == nil {
		t.Fatal("original name still resolves")
	}
	for _, tab := range s.tabs {
		if tab.data == dataDoc || tab.object == objectDoc {
			t.Fatal("rename retained original scoped documents")
		}
	}
	if query.Editor.Buffer.String() != "SELECT unsaved_original_rename_sql" {
		t.Fatal("rename changed SQL document")
	}
	waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
	found := false
	for _, object := range s.sessions.Current().Cache[schema] {
		if object.Name == "original" {
			t.Fatal("old navigation name retained")
		}
		found = found || object.Name == destination
	}
	if !found {
		t.Fatal("new navigation name absent")
	}
}
