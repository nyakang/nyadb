package shell

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

func TestGaussDBSessionAndSharedGridDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_GAUSSDB_IT") != "1" {
		t.Skip("requires disposable GaussDB fixture")
	}
	if output := os.Getenv("NYADB_GAUSSDB_VISUAL_DIR"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_GAUSSDB_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.GaussDB)
	p.Name, p.Host, p.Port, p.Database, p.Username = "GaussDB live grid", "127.0.0.1", uint16(port), "nyadb_gaussdb_it", "nyadb_it"
	p.TLSMode = "disable"
	if os.Getenv("NYADB_GAUSSDB_CA_FILE") != "" {
		p.TLSMode = "verify-full"
	}
	p.PostgreSQLOptions.CAFile = os.Getenv("NYADB_GAUSSDB_CA_FILE")
	p, e = service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = os.Getenv("NYADB_GAUSSDB_PASSWORD")
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if e = tt.Click("连接所选数据库"); e != nil {
		t.Fatal(e)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatalf("GaussDB UI did not settle: status=%s pending=%s error=%s databases=%v selected=%s errors=%v", s.sessions.Current().Status, s.sessions.Current().Pending, s.sessions.Current().Error, s.sessions.Current().Databases, s.sessions.Current().Database, s.sessions.Current().Errors)
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.GaussDB {
		t.Fatalf("GaussDB UI connect: %s %s", w.Status, w.Error)
	}
	schema := "nyadb_ui_gauss_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	session, e := s.engineSession(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := session.Reserve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, e = seed.Execute(ctx, "CREATE SCHEMA "+schema+"; CREATE TABLE "+schema+".records(id BIGINT PRIMARY KEY,note TEXT,amount NUMERIC(40,20)); INSERT INTO "+schema+".records VALUES(1,'alpha',9007199254740993.12345678901234567890),(2,NULL,2); CREATE SEQUENCE "+schema+".fixture_sequence START 123")
	seed.Close()
	if e != nil {
		t.Fatal(e)
	}
	cleaned := false
	cleanupFixture := func() {
		if cleaned {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lease, err := session.Reserve(cleanup)
		if err != nil {
			t.Error(err)
			return
		}
		defer lease.Close()
		if _, err = lease.Execute(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		} else {
			cleaned = true
		}
	}
	defer cleanupFixture()
	s.navigation.Filter.Engine = connections.GaussDB
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.MySQL, Name: p.Name}) {
		t.Fatal("GaussDB filter merged product identities")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, schema))
	wait(func() bool { return w.Pending == "" && w.Cache[schema] != nil })
	if !s.toolbarActionAvailable("函数") || !s.toolbarActionAvailable("其它") || s.toolbarActionAvailable("用户") || s.toolbarActionAvailable("备份") {
		t.Fatal("read-only program browsing or unsupported tools have incorrect availability")
	}
	for _, object := range w.Cache[schema] {
		if object.Name == "private_sequence" {
			t.Fatal("sequence without privileges appeared in the browser")
		}
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Object: "records", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != schema {
		t.Fatal("GaussDB routed to a different data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("GaussDB native grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved by GaussDB grid"})
	tt.Frame()
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by GaussDB grid" {
		t.Fatal("GaussDB grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_GAUSSDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "gaussdb-live-grid", tt)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Object: "fixture_sequence", Category: "序列"})
	sequence := s.currentTab()
	if sequence.kind != tabProgram || sequence.object == nil || sequence.object.ProgramData().Sequence == nil || sequence.object.ProgramData().Sequence.DefinitionFromServer {
		t.Fatal("sequence did not open a native read-only document")
	}
	tt.Frame()
	if !tt.HasText("序列定义（根据服务器目录参数重建）") {
		t.Fatal("native definition mislabeled")
	}
	if output := os.Getenv("NYADB_GAUSSDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "gaussdb-live-sequence", tt)
	}
	s.openQuery()
	sqlTab := s.currentTab()
	sql := "SELECT note,amount FROM records WHERE id=1"
	sqlTab.query.Editor.Buffer.Set(sql)
	sqlTab.query.Editor.Select(0, len(sql))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	result := sqlTab.query.Result()
	if result == nil || result.Rows[0][0] != "saved by GaussDB grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("GaussDB SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, schema).Dialect != "postgres" || s.sqlNamespace(p.ID, schema).Catalog != p.Database {
		t.Fatal("GaussDB lost PostgreSQL dialect or namespace")
	}
	scriptSchema := "nyadb_ui_script_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	script := "CREATE SCHEMA \"" + scriptSchema + "\"; CREATE PROCEDURE \"" + scriptSchema + "\".p() AS BEGIN IF true THEN NULL; END IF; END;\n/\nSELECT 2;"
	sqlTab.query.Editor.Buffer.Set(script)
	sqlTab.query.Editor.Select(0, len(script))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	if r := sqlTab.query.Result(); sqlTab.query.Error() != "" || r == nil || len(r.Steps) != 3 {
		t.Fatal("workbench native script failed", sqlTab.query.Error())
	}
	scriptCleaned := false
	cleanupScript := func() {
		if scriptCleaned {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		session, err := s.engineSession(p.ID)
		if err != nil {
			t.Error(err)
			return
		}
		lease, err := session.Reserve(cleanup)
		if err != nil {
			t.Error(err)
			return
		}
		defer lease.Close()
		if _, err = lease.Execute(cleanup, "DROP SCHEMA \""+scriptSchema+"\" CASCADE"); err != nil {
			t.Error(err)
		} else {
			scriptCleaned = true
		}
	}
	defer cleanupScript()
	body := "CREATE OR REPLACE PROCEDURE \"" + scriptSchema + "\".p() AS BEGIN IF true THEN NULL; END IF; END;"
	sql = body + "\n/\nSELECT missing_column FROM absent_table;"
	sqlTab.query.Editor.Buffer.Set(sql)
	caret := strings.Index(sql, "NULL") + 2
	sqlTab.query.Editor.Select(caret, caret)
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	if r := sqlTab.query.Result(); sqlTab.query.Error() != "" || r == nil || len(r.Steps) != 1 || r.Steps[0].SQL != body {
		t.Fatal("workbench caret fragmented/replayed script", sqlTab.query.Error())
	}
	fileText := "CALL \"" + scriptSchema + "\".p(); SELECT 42;"
	s.SetSQLPersistence(nil, sqlworkspace.Files{
		OpenPath: func() (string, error) { return "native-script.sql", nil },
		OpenReader: func(string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fileText)), nil
		},
	}, nil)
	tt.Frame()
	if e = tt.Click("执行 SQL 文件"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	if r := sqlTab.query.Result(); sqlTab.query.Error() != "" || r == nil || len(r.Steps) != 2 || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("workbench SQL file failed or changed editor text", sqlTab.query.Error())
	}
	if e = tt.Click("结果 2"); e != nil {
		t.Fatal(e)
	}
	if !tt.HasText("42") {
		t.Fatal("SQL file result was not displayed")
	}
	if output := os.Getenv("NYADB_GAUSSDB_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "gaussdb-live-script", tt)
	}
	cleanupScript()
	// Cancellation is handled by the native owning lease, then UI recovery.
	cancelSQL := "SELECT pg_sleep(30); SELECT 987654321;"
	sqlTab.query.Editor.Buffer.Set(cancelSQL)
	sqlTab.query.Editor.Select(0, len(cancelSQL))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return sqlTab.query.Loading() })
	time.Sleep(100 * time.Millisecond)
	if e = tt.Click("取消"); e != nil {
		t.Fatal(e)
	}
	waitSQLUI(t, s, tt, sqlTab)
	if !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != cancelSQL {
		t.Fatal("canceled SQL silently recovered or lost text")
	}
	if e = tt.Click("建立新物理会话（不执行现有 SQL）"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !sqlTab.query.NeedsRecovery() })
	if sqlTab.query.Editor.Buffer.String() != cancelSQL || sqlTab.query.Loading() {
		t.Fatal("recovery replayed SQL")
	}
	sql = cancelSQL
	cleanupFixture()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || !sqlTab.query.NeedsRecovery() || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("GaussDB disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect left scoped data document")
		}
	}
}
