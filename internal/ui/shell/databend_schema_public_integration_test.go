package shell

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
)

func TestDatabendNativePublicSchemaRenamePreviewReceiptAndParkedCleanup(t *testing.T) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	s, tt, p, host := databendSchemaPublicState(t, f)
	services := *host
	name, destination := `public_%猫"original`, `public_%猫"renamed`
	qualified := func(name string) string { return schemaddl.Quote(f.Database) + "." + schemaddl.Quote(name) }
	query := func(sql string) [][]string { return databendWorkbenchSQL(t, wf, sql) }
	query("CREATE TABLE " + qualified(name) + " (id UInt64 NOT NULL,v Binary,txt String DEFAULT 'retained')")
	query("INSERT INTO " + qualified(name) + " (id,v) VALUES (18446744073709551615,unhex('00ff80'))")
	before := query("SELECT id,hex(v),txt FROM " + qualified(name))
	data := databendWorkbenchData(t, s, tt, services, p, name)
	s.openMySQLStructure(services, f.Database, name)
	structure := s.currentTab().object
	databendWorkbenchWait(t, tt, func() bool { return !structure.Loading() })
	other := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	activeID := s.currentTab().id
	workspace := s.objectWorkspace
	if !workspace.CanRenameTable(p.ID, f.Database, name) || !workspace.CanDropTable(p.ID, f.Database, name) || !workspace.CanClearTable(p.ID, f.Database, name) || !workspace.CanCopyTable(p.ID, f.Database, name) || workspace.CanCreateTable(p.ID, f.Database) || workspace.CanChangeTable(p.ID) {
		t.Fatal("public object capability exposure differs from implemented rename/drop/copy")
	}
	if ops := s.runtime.SchemaDesign(p.ID); ops.Rename != nil || ops.Apply != nil || ops.Drop != nil {
		t.Fatal("session-level schema executor exposed")
	}
	openPreview := func() {
		workspace.BeginTableChange(p.ID, f.Database, name, mysqlsvc.MySQLTableRename)
		tt.Frame()
		if !workspace.TableDialogOpen() {
			t.Fatal("public rename modal missing")
		}
		databendWorkbenchClick(t, tt, "重新读取结构")
		databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
		if workspace.TableError() != "" {
			t.Fatal("native original snapshot", workspace.TableError())
		}
		setTestInput(t, tt, "目标表名称", destination)
		tt.Frame()
	}
	openPreview()
	databendWorkbenchCapture(t, wf, tt, "rename-preview", "目标表名称", "确认重命名表", "取消")
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || !reflect.DeepEqual(before, query("SELECT id,hex(v),txt FROM "+qualified(name))) {
		t.Fatal("preview cancellation renamed or changed original rows")
	}
	openPreview()
	originalGeneration := s.runtime.Generation(p.ID)
	databendWorkbenchClick(t, tt, "确认重命名表")
	originalTab := s.activeTab
	s.activeTab = 0
	s.selectedProfileID = p.ID + 10000
	s.sessions.Activate(p.ID + 10000)
	deadline := time.Now().Add(60 * time.Second)
	for {
		rows := query("SELECT name FROM system.tables WHERE database='" + f.Database + "' AND name='" + destination + "'")
		tt.Frame()
		if len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native rename did not reach original database")
		}
	}
	if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("rename") != nil {
		t.Fatal("another profile consumed parked original acknowledgement")
	}
	s.activeTab = originalTab
	s.selectedProfileID = p.ID
	s.sessions.Activate(p.ID)
	databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
	out := workspace.SchemaTableOriginalOutcome("rename")
	if out == nil || out.State != "committed" || !out.Verified || out.Error != nil || out.Receipt == nil || out.Actual == nil {
		t.Fatal("public rename failed", out, workspace.TableError())
	}
	if out.Receipt.Original.Target.Generation != originalGeneration || out.Receipt.Original.Target.Object != s.runtime.Namespace(p.ID, f.Database).Object("table", name) || out.Receipt.Destination != destination || out.Actual.Schema.Databend.TableID != out.Receipt.Original.Schema.Databend.TableID {
		t.Fatal("public generation or original identity changed")
	}
	after := query("SELECT id,hex(v),txt FROM " + qualified(destination))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("public rename changed exact native rows", before, after)
	}
	for _, tab := range s.tabs {
		if tab.data == data || tab.object == structure {
			t.Fatal("old name scoped document survived accepted rename")
		}
	}
	if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(p.ID) {
		t.Fatal("rename disturbed unrelated active document or session")
	}
	databendWorkbenchCapture(t, wf, tt, "rename-receipt", "复制完整回执", "取消")
	databendWorkbenchClick(t, tt, "复制完整回执")
	var exported struct {
		Kind    string
		Receipt contract.SchemaDesignWriteReceipt
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &exported) != nil || exported.Kind != "schema-original-write-receipt" || !reflect.DeepEqual(exported.Receipt, *out.Receipt) {
		t.Fatal("clipboard did not retain complete original schema receipt", tt.Clipboard())
	}
	receipt := exported.Receipt
	out.Receipt.NativeJSON = "caller mutation"
	if workspace.SchemaTableOriginalOutcome("rename").Receipt.NativeJSON != receipt.NativeJSON {
		t.Fatal("caller rewrote original receipt")
	}
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("rename") != nil {
		t.Fatal("terminal close retained modal/receipt")
	}
	databendWorkbenchCapture(t, wf, tt, "rename-complete")
	f.Journal(t, "public-schema.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "receipt": receipt, "before": before, "after": after, "preview_cancelled": true, "parked_retained": true, "original_tabs_closed": true, "unrelated_tab_preserved": true, "processes": f.Starts.Load()})
}

func databendSchemaPublicState(t *testing.T, f *databendfixture.Fixture) (*State, *ui.Tester, connections.Profile, *ui.Services) {
	t.Helper()
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.Import(context.Background(), driverstore.Product{Key: "databend", Runtime: "java", MainClass: "com.dbx.agent.databend.DatabendAgent"}, f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), "databend", f.Version); err != nil {
			t.Error(err)
		}
	})
	s, _, profiles, _ := profileTestState(t)
	dbruntime.RegisterBuiltins(s.runtime, nil)
	endpoint, _ := url.Parse(f.Endpoint)
	uri := &url.URL{Scheme: "databend", Host: endpoint.Host, Path: "/" + f.Database, RawQuery: "ssl=false", User: url.User(f.Username)}
	parsed, err := connections.ParseURI(uri.String())
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profile
	p.Name = "Public Databend schema"
	p.DatabendOptions.DriverVersion, p.DatabendOptions.DriverSHA256, p.DatabendOptions.DriverDirectory = driver.Version, driver.SHA256, driver.Directory
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	observer := f.Observe(directory)
	s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		return s.runtime.ConnectSession(driverstore.WithRuntimeObserver(ctx, observer), p, secret)
	}, Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected})
	s.sessions.Current().Password = f.Secret(t)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	databendWorkbenchClick(t, tt, "连接所选数据库")
	databendWorkbenchWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("public schema session failed", s.sessions.Current().Error)
	}
	return s, tt, p, &services
}
