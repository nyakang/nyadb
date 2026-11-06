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

func TestOpenGaussTableCopyMenusPreviewTypedValuesAndSourceTabsFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires disposable openGauss fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENGAUSS_PORT"), 10, 16)
	if err != nil || port < 1024 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name, p.Host, p.Port, p.Database, p.Username = "openGauss table copy", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
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
	schema := fmt.Sprintf("nyadb_gauss_ui_copy_%d", time.Now().UnixNano())
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
	s.openMySQLStructure(services, schema, "original")
	objectDoc := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !objectDoc.Loading() })
	s.openQuery()
	query := s.currentTab().query
	query.Editor.Buffer.Set("SELECT unsaved_original_copy_sql")
	for _, data := range []bool{true, false} {
		destination := fmt.Sprintf("copied_%t", data)
		s.activeTab = 0
		s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Category: "表", Object: "original"})
		tt.Frame()
		if err := tt.RightClick("original"); err != nil {
			t.Fatal(err)
		}
		label := "仅结构"
		if data {
			label = "结构和数据"
		}
		if err := tt.ChooseMenuItem("复制表", label); err != nil {
			t.Fatal(err)
		}
		shellDropPreview(t, tt)
		waitTableChange(t, s, tt)
		setTestInput(t, tt, "目标表名称", destination)
		if s.objectWorkspace.TableError() != "" {
			t.Fatal(s.objectWorkspace.TableError())
		}
		if tt.HasText("保留隐藏 rowid") {
			t.Fatal("SQLite physical identity option shown for openGauss")
		}
		if err := tt.Click("复制结构 SQL 预览"); err != nil {
			t.Fatal(err)
		}
		tt.Command("selectAll")
		tt.Command("copy")
		preview := tt.Clipboard()
		if !strings.Contains(preview, "INCLUDING ALL EXCLUDING INDEXES") || !strings.Contains(preview, ns+`."`+destination+`"`) {
			t.Fatal("native qualified copy preview missing")
		}
		if strings.Contains(preview, "INSERT INTO") != data {
			t.Fatal("copy menu lost data choice")
		}
		target := s.runtime.Namespace(p.ID, schema).Object("table", destination)
		if _, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10}); err == nil {
			t.Fatal("copy preview created destination")
		}
		if data && os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR") != "" {
			writeDomainCapture(t, os.Getenv("NYADB_OPENGAUSS_VISUAL_DIR"), "opengauss-live-table-copy-preview", tt)
		}
		tt.SetSize(840, 560)
		tt.Frame()
		if err := tt.Click("确认复制表"); err != nil {
			t.Fatal(err)
		}
		waitTableChange(t, s, tt)
		if s.objectWorkspace.TableDialogOpen() || s.objectWorkspace.TableError() != "" {
			t.Fatal("native copy did not settle", s.objectWorkspace.TableError())
		}
		page, err := session.Page(ctx, contract.PageRequest{Object: target, Limit: 10})
		count := 0
		if data {
			count = 1
		}
		if err != nil || len(page.Rows) != count {
			t.Fatal("copied native grid rows", err)
		}
		if data && page.Rows[0][2].Text != "9007199254740993.12345678901234567890" {
			t.Fatal("copy lost precise numeric data")
		}
		waitSQLiteDesigner(t, tt, func() bool { return s.sessions.Current().Pending == "" })
		found := false
		for _, o := range s.sessions.Current().Cache[schema] {
			found = found || o.Name == destination
		}
		if !found {
			t.Fatal("verified destination not published to navigation")
		}
		tt.SetSize(1400, 900)
	}
	keptData, keptObject := false, false
	for _, tab := range s.tabs {
		keptData = keptData || tab.data == dataDoc
		keptObject = keptObject || tab.object == objectDoc
	}
	if !keptData || !keptObject || query.Editor.Buffer.String() != "SELECT unsaved_original_copy_sql" {
		t.Fatal("copy changed original documents")
	}
}
