package shell

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
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

func TestDamengSessionAndSharedGridDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires disposable Dameng fixture")
	}
	if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
		if err := os.MkdirAll(output, 0755); err != nil {
			t.Fatal(err)
		}
	}
	p, password := damengUIFixture(t)
	s, _, service, _ := profileTestState(t)
	p.Name = "Dameng live grid"
	var e error
	p, e = service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	if e = vault.Set(p.ID, "", password); e != nil {
		t.Fatal(e)
	}
	s.SetCredentialVault(vault)
	s.SetConnectionWindowHost(func() {}, func() {})
	s.startEditProfile()
	editor := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if e = editor.Click("测试连接"); e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(15 * time.Second); !editor.HasText("达梦 连接测试成功（已关闭测试会话）") && time.Now().Before(deadline); {
		editor.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if !editor.HasText("达梦 连接测试成功（已关闭测试会话）") || s.runtime.IsConnected(p.ID) {
		t.Fatal("native editor test did not close its independent session", s.connectionEditor.Error())
	}
	s.closeConnectionDialog()
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	if e = tt.Click("连接所选数据库"); e != nil {
		t.Fatal(e)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatalf("Dameng UI did not settle: status=%s pending=%s error=%s databases=%v selected=%s errors=%v", s.sessions.Current().Status, s.sessions.Current().Pending, s.sessions.Current().Error, s.sessions.Current().Databases, s.sessions.Current().Database, s.sessions.Current().Errors)
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.Dameng {
		t.Fatalf("Dameng UI connect: %s %s", w.Status, w.Error)
	}
	schema := "NYADB_UI_DAMENG_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	published, e := s.runtime.Session(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	session, e := contract.Require[contract.QueryProvider](published)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := session.ReserveQuery(9001)
	if e != nil {
		t.Fatal(e)
	}
	_, e = seed.Execute(ctx, "SYSDBA", "CREATE SCHEMA "+schema+" AUTHORIZATION SYSDBA; CREATE TABLE "+schema+".records(id BIGINT PRIMARY KEY,note VARCHAR(128),amount DECIMAL(38,20)); INSERT INTO "+schema+".records VALUES(1,'alpha',9007199254740993.12345678901234567890); INSERT INTO "+schema+".records VALUES(2,NULL,2); CREATE SEQUENCE "+schema+".fixture_sequence START WITH 123")
	seed.Release()
	if e != nil {
		t.Fatal(e)
	}
	cleaned := false
	cleanupFixture := func() {
		if cleaned {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		lease, err := session.ReserveQuery(9002)
		if err != nil {
			t.Error(err)
			return
		}
		defer lease.Release()
		if _, err = lease.Execute(cleanup, "SYSDBA", "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		} else {
			cleaned = true
		}
	}
	defer cleanupFixture()
	s.sessions.RefreshDatabases(services.Invalidate)
	wait(func() bool { return w.Pending == "" && w.Namespaces[schema].Schema == schema })
	s.navigation.Filter.Engine = connections.Dameng
	if !s.navigation.ProfileVisible(p) || s.navigation.ProfileVisible(connections.Profile{Kind: connections.MySQL, Name: p.Name}) {
		t.Fatal("Dameng filter merged product identities")
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
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Object: "RECORDS", Category: "表"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Catalog != p.Database || dataTab.data.Object().Schema != schema {
		t.Fatal("Dameng routed to a different data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal("Dameng native grid", dataTab.data.Error())
	}
	s.dataWorkspace.EditCell(services, dataTab.data, 0, "NOTE", contract.Cell{Kind: contract.Text, Text: "saved by Dameng grid"})
	tt.Frame()
	if e = tt.Click("保存数据更改"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page().Rows[0][1].Text != "saved by Dameng grid" {
		t.Fatal("Dameng grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "dameng-live-grid", tt)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: schema, Object: "FIXTURE_SEQUENCE", Category: "序列"})
	sequence := s.currentTab()
	if sequence.kind != tabProgram || sequence.object == nil || sequence.object.ProgramData().Definition == "" || sequence.object.ProgramData().Ref != s.runtime.Namespace(p.ID, schema).Object("sequence", "FIXTURE_SEQUENCE") {
		t.Fatal("sequence did not open a native read-only document")
	}
	tt.Frame()
	if !tt.HasText("只读") {
		t.Fatal("native definition not shown")
	}
	if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "dameng-live-sequence", tt)
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
	wait(func() bool { return !sqlTab.query.Loading() })
	result := sqlTab.query.Result()
	if result == nil || result.Rows[0][0] != "saved by Dameng grid" || result.Sets[0].Rows[0][1].Kind != contract.Decimal {
		t.Fatal("Dameng SQL execution", sqlTab.query.Error())
	}
	if s.sqlCompletionCatalog(p.ID, schema).Dialect != "dameng" || s.sqlNamespace(p.ID, schema).Catalog != p.Database {
		t.Fatal("Dameng lost native dialect or namespace")
	}
	scriptSchema := "nyadb_ui_script_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	script := "CREATE SCHEMA \"" + scriptSchema + "\" AUTHORIZATION SYSDBA; CREATE PROCEDURE \"" + scriptSchema + "\".p AS BEGIN IF 1=1 THEN NULL; END IF; END;\n/\nSELECT 2;"
	sqlTab.query.Editor.Buffer.Set(script)
	sqlTab.query.Editor.Select(0, len(script))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !sqlTab.query.Loading() })
	if r := sqlTab.query.Result(); sqlTab.query.Error() != "" || r == nil || len(r.Steps) != 3 {
		t.Fatal("workbench native script failed", sqlTab.query.Error())
	}
	scriptCleaned := false
	cleanupScript := func() {
		if scriptCleaned {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		published, err := s.runtime.Session(p.ID)
		if err != nil {
			t.Error(err)
			return
		}
		session, err := contract.Require[contract.QueryProvider](published)
		if err != nil {
			t.Error(err)
			return
		}
		lease, err := session.ReserveQuery(9002)
		if err != nil {
			t.Error(err)
			return
		}
		defer lease.Release()
		if _, err = lease.Execute(cleanup, "SYSDBA", "DROP SCHEMA \""+scriptSchema+"\" CASCADE"); err != nil {
			t.Error(err)
		} else {
			scriptCleaned = true
		}
	}
	defer cleanupScript()
	body := "CREATE OR REPLACE PROCEDURE \"" + scriptSchema + "\".p AS BEGIN IF 1=1 THEN NULL; END IF; END;"
	sql = body + "\n/\nSELECT missing_column FROM absent_table;"
	sqlTab.query.Editor.Buffer.Set(sql)
	caret := strings.Index(sql, "NULL") + 2
	sqlTab.query.Editor.Select(caret, caret)
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !sqlTab.query.Loading() })
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
	wait(func() bool { return !sqlTab.query.Loading() })
	if r := sqlTab.query.Result(); sqlTab.query.Error() != "" || r == nil || len(r.Steps) != 2 || sqlTab.query.Editor.Buffer.String() != sql {
		t.Fatal("workbench SQL file failed or changed editor text", sqlTab.query.Error())
	}
	if e = tt.Click("结果 2"); e != nil {
		t.Fatal(e)
	}
	if !tt.HasText("42") {
		t.Fatal("SQL file result was not displayed")
	}
	if output := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); output != "" {
		writeDomainCapture(t, output, "dameng-live-script", tt)
	}
	cleanupScript()
	// Cancellation is handled by the native owning lease, then UI recovery.
	cancelSQL := "SELECT COUNT(*) FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0; SELECT 987654321;"
	sqlTab.query.Editor.Buffer.Set(cancelSQL)
	sqlTab.query.Editor.Select(0, len(cancelSQL))
	tt.Frame()
	if e = tt.Click("运行当前或选中 SQL"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return sqlTab.query.Loading() })
	time.Sleep(300 * time.Millisecond)
	if e = tt.Click("取消"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !sqlTab.query.Loading() })
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
		t.Fatal("Dameng disconnect discarded SQL or retained session")
	}
	for _, tab := range s.tabs {
		if tab.data != nil && tab.profileID() == p.ID {
			t.Fatal("disconnect left scoped data document")
		}
	}
	s.deleteProfileID(p.ID)
	if _, found, err := vault.Get(p.ID, ""); err != nil || found {
		t.Fatal("deleted native profile left its credential", err)
	}
}

// Only the explicit disposable fixture runner supplies this private file.
// The password remains in a local file, never a command-line argument.
func damengUIFixture(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires owned DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Endpoint     string `json:"endpoint"`
		PasswordFile string `json:"password_file"`
		SSL          string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("captured loopback fixture required")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		t.Fatal("explicit native fixture port required")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Dameng)
	p.Name, p.Host, p.Port, p.TLSMode = "Dameng fixture", host, uint16(n), "disable"
	a := &p.DamengOptions
	a.LibraryFile = os.Getenv("NYADB_DAMENG_DPI_LIBRARY")
	a.MaxConnections, a.SQLMaxConnections = 2, 3
	if f.SSL != "" {
		p.TLSMode = "verify-full"
		a.CAFile = filepath.Join(f.SSL, "ca-cert.pem")
		a.CertificateFile = filepath.Join(f.SSL, "client-cert.pem")
		a.KeyFile = filepath.Join(f.SSL, "client-key.pem")
	}
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, string(secret)
}
