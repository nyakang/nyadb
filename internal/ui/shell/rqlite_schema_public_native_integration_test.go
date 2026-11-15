package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

// Holds only the native acknowledgement to make parking deterministic. Product
// descriptors, provider features, runtime publication and shell callbacks are real.
func rqliteSchemaPublicShell(t *testing.T, p connections.Profile, password string) (*State, *ui.Tester, *ui.Services, connections.Profile, <-chan contract.SchemaDesignOutcome, chan struct{}) {
	t.Helper()
	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	observed, release := make(chan contract.SchemaDesignOutcome, 1), make(chan struct{})
	s.runtime.Register(connections.RQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		raw, err := rqlite.OpenSession(ctx, p, secret)
		if err != nil {
			return nil, err
		}
		return &rqliteSchemaAdapterFixture{Session: raw.(*rqlite.Session), observed: observed, release: release}, nil
	}))
	p.Name = "original public schema"
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	if password != "" {
		if err := vault.Set(p.ID, "", password); err != nil {
			t.Fatal(err)
		}
	}
	s.SetCredentialVault(vault)
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 950)
	tt.Frame()
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Status != "连接中" })
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("real public RQLite session failed", s.sessions.Current().Error)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return s, tt, &services, p, observed, release
}

func waitRQLiteSchemaPublic(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for !done() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if !done() {
		t.Fatal("original public schema operation did not settle")
	}
}

func rqliteSchemaPublicQuery(t *testing.T, session contract.Session) func(string) contract.SQLResult {
	t.Helper()
	p, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.ReserveQuery(889)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return func(sql string) contract.SQLResult {
		t.Helper()
		out, err := l.Execute(context.Background(), "main", sql)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func rqliteSchemaPublicReceipt(t *testing.T, tt *ui.Tester, s *objectworkspace.State, out *contract.SchemaDesignOutcome, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click("复制完整回执"); err != nil {
		t.Fatal(err)
	}
	encoded := tt.Clipboard()
	var record struct {
		Receipt contract.SchemaDesignWriteReceipt
	}
	if json.Unmarshal([]byte(encoded), &record) != nil || !reflect.DeepEqual(record.Receipt, *out.Receipt) {
		t.Fatal("public schema clipboard changed the complete original receipt")
	}
	files := make(chan string, 1)
	s.SetSchemaReceiptFiles(func(string) (string, error) { return "receipt.json", nil }, func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		files <- b.String()
		return err
	})
	tt.Frame()
	if err := tt.Click("导出完整回执"); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-files:
		if raw != encoded {
			t.Fatal("export changed complete original receipt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original public receipt export did not finish")
	}
	if folder := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); folder != "" {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "schema-public-"+label+"-receipt.json"), []byte(encoded), 0600); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, folder, "schema-public-"+label, tt)
	}
}

func TestRQLiteNativePublicSchemaDropRenameParkedReceiptAndOriginalShellCleanup(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite public schema fixtures")
	}
	for _, operation := range []string{"drop", "rename", "drop-inbound-rejected"} {
		t.Run(operation, func(t *testing.T) {
			parsed, err := connections.ParseURI(rqlitePublicFixture(t, "none"))
			if err != nil {
				t.Fatal(err)
			}
			s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "")
			published, err := s.runtime.Session(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			query := rqliteSchemaPublicQuery(t, published)
			query("CREATE TABLE native_public_schema(id INTEGER PRIMARY KEY,v BLOB); INSERT INTO native_public_schema VALUES(9007199254740993,X'00FF0100'),(9223372036854775807,X'FE00'); CREATE TABLE other(id INTEGER PRIMARY KEY)")
			if operation == "drop-inbound-rejected" {
				query("CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES native_public_schema(id)); INSERT INTO child VALUES(1,9007199254740993)")
			}
			before := query("SELECT id,v FROM native_public_schema ORDER BY id").Sets[0].Rows
			s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
			waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			workspace := s.objectWorkspace
			if !workspace.CanCreateTable(p.ID, "main") || !workspace.CanDropTable(p.ID, "main", "native_public_schema") || !workspace.CanRenameTable(p.ID, "main", "native_public_schema") || workspace.CanCopyTable(p.ID, "main", "native_public_schema") || workspace.CanChangeTable(p.ID) {
				t.Fatal("public operation discovery omitted supported tools or advertised unsupported tools")
			}
			if ops := s.runtime.SchemaDesign(p.ID); ops.Apply != nil || ops.Drop != nil || ops.Rename != nil || ops.Copy != nil {
				t.Fatal("real public schema session acquired write executors")
			}
			s.openMySQLData(*services, "main", "native_public_schema")
			data := s.currentTab().data
			waitRQLiteSchemaPublic(t, tt, func() bool { return !data.Loading() })
			s.openMySQLStructure(*services, "main", "native_public_schema")
			structure := s.currentTab().object
			waitRQLiteSchemaPublic(t, tt, func() bool { return !structure.Loading() })
			s.openMySQLData(*services, "main", "other")
			other := s.currentTab().data
			waitRQLiteSchemaPublic(t, tt, func() bool { return !other.Loading() })
			activeID := s.currentTab().id
			action, key := mysqlsvc.MySQLTableDrop, "drop"
			if operation == "rename" {
				action, key = mysqlsvc.MySQLTableRename, "rename"
			}
			workspace.BeginTableChange(p.ID, "main", "native_public_schema", action)
			tt.Frame()
			if !workspace.TableDialogOpen() {
				t.Fatal("original document-only operation did not open its actual modal")
			}
			if err := tt.Click("重新读取结构"); err != nil {
				t.Fatal(err)
			}
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			if !reflect.DeepEqual(before, query("SELECT id,v FROM native_public_schema ORDER BY id").Sets[0].Rows) {
				t.Fatal("preview changed exact int64 or BLOB rows")
			}
			label := "确认删除表"
			if operation == "rename" {
				setTestInput(t, tt, "目标表名称", "native renamed table")
				label = "确认重命名表"
			} else {
				setTestInput(t, tt, "确认原表名称", "native_public_schema")
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			originalGeneration := s.sessions.Current().Generation
			originalActiveTab := s.activeTab
			s.activeTab = 0
			s.selectedProfileID = p.ID + 10000
			s.sessions.Activate(p.ID + 10000)
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(20 * time.Second):
				t.Fatal("native original write did not acknowledge")
			}
			if native.Receipt == nil {
				t.Fatal("original native table operation returned no complete receipt", native.State, native.Error)
			}
			close(release)
			for i := 0; i < 5; i++ {
				tt.Frame()
			}
			if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome(key) != nil || s.sessions.Current().ProfileID == p.ID {
				t.Fatal("another active profile consumed the original modal receipt")
			}
			s.activeTab = originalActiveTab
			s.selectedProfileID = p.ID
			s.sessions.Activate(p.ID)
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			out := workspace.SchemaTableOriginalOutcome(key)
			if out == nil || out.Receipt == nil || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Sequence != 1 || out.Receipt.Original.Target.Generation != published.Generation() || out.Receipt.Original.Target.Object != s.runtime.Namespace(p.ID, "main").Object("table", "native_public_schema") || s.sessions.Current().Generation != originalGeneration {
				t.Fatal("public runtime/UI lost original receipt, namespace or generations", out)
			}
			if operation == "drop-inbound-rejected" {
				if out.State != "rolled_back" || out.Verified || out.Error == nil || !reflect.DeepEqual(before, query("SELECT id,v FROM native_public_schema ORDER BY id").Sets[0].Rows) {
					t.Fatal("inbound dependency rejection lost original data", out.State, out.Error)
				}
				if err := tt.Click("查看原始结构回执"); err != nil {
					t.Fatal(err)
				}
			} else {
				if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || !workspace.TableDialogOpen() {
					t.Fatal("public native operation did not retain its successful receipt", out.State, out.Error)
				}
				if operation == "rename" {
					if out.Receipt.Destination != "native renamed table" || out.Actual.Target.Object.Name != "native renamed table" || !reflect.DeepEqual(before, query(`SELECT id,v FROM "native renamed table" ORDER BY id`).Sets[0].Rows) {
						t.Fatal("rename changed complete original rows or destination")
					}
				} else if out.Actual.Schema.Exists || out.Actual.Target != out.Receipt.Original.Target {
					t.Fatal("drop receipt lost its original absent target")
				}
				for _, tab := range s.tabs {
					if tab.data == data || tab.object == structure {
						t.Fatal("shell kept original scoped tabs after committed operation")
					}
				}
			}
			if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(p.ID) {
				t.Fatal("original cleanup changed unrelated active document or closed a healthy session")
			}
			rqliteSchemaPublicReceipt(t, tt, workspace, out, operation)
			out.Receipt.NativeJSON = "caller mutation"
			if workspace.SchemaTableOriginalOutcome(key).Receipt.NativeJSON != native.Receipt.NativeJSON {
				t.Fatal("caller mutation rewrote cached original receipt")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome(key) != nil {
				t.Fatal("explicit modal close retained original receipt cache")
			}
		})
	}
}

func TestRQLiteNativePublicQueryOnlySchemaRejectionRetainsOriginalReceiptAndDraft(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite query-only schema fixture")
	}
	parsed, err := connections.ParseURI(rqlitePublicFixture(t, "roles"))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Profile.ID = 881
	admin, err := rqlite.OpenSession(context.Background(), parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	query := rqliteSchemaPublicQuery(t, admin)
	query("CREATE TABLE native_query_only_schema(id INTEGER PRIMARY KEY,v BLOB); INSERT INTO native_query_only_schema VALUES(9007199254740993,X'00FF')")
	before := query("SELECT id,v FROM native_query_only_schema").Sets[0].Rows
	parsed.Profile.ID, parsed.Profile.Username = 0, "query-only"
	s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "query-only-password")
	s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
	waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
	s.openMySQLStructure(*services, "main", "native_query_only_schema")
	d := s.currentTab().object
	waitRQLiteSchemaPublic(t, tt, func() bool { return !d.Loading() })
	s.objectWorkspace.BeginDesign(d, *services)
	waitRQLiteSchemaPublic(t, tt, func() bool { return !d.DesignBusy() && tt.HasText("添加字段") })
	if err := tt.Click("添加字段"); err != nil {
		t.Fatal(err)
	}
	if err := s.objectWorkspace.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认执行结构 SQL"); err != nil {
		t.Fatal(err)
	}
	s.activeTab = 0
	var native contract.SchemaDesignOutcome
	select {
	case native = <-observed:
	case <-time.After(20 * time.Second):
		t.Fatal("real query-only server did not return its original rejection")
	}
	if native.Receipt == nil {
		t.Fatal("native query-only rejection returned no complete receipt", native.State, native.Error)
	}
	close(release)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if !d.DesignBusy() || d.SchemaDesignOriginalOutcome() != nil {
		t.Fatal("parked query-only receipt consumed outside its original document")
	}
	s.activeTab = 1
	waitRQLiteSchemaPublic(t, tt, func() bool { return !d.DesignBusy() })
	out := d.SchemaDesignOriginalOutcome()
	if out == nil || out.State != "not_executed" || out.Verified || out.Error == nil || out.Receipt == nil || out.Receipt.Sequence != 1 || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Original.Target.Generation != s.runtime.Generation(p.ID) || !d.DesignDirty() || !s.runtime.IsConnected(p.ID) {
		t.Fatal("native permission rejection became unknown, erased drafts or lost original receipt", out)
	}
	var rejection struct {
		HTTPStatus int `json:"http_status"`
	}
	if json.Unmarshal([]byte(out.Receipt.NativeJSON), &rejection) != nil || rejection.HTTPStatus != 401 && rejection.HTTPStatus != 403 {
		t.Fatal("query-only rejection lacks complete original HTTP evidence")
	}
	if !reflect.DeepEqual(before, query("SELECT id,v FROM native_query_only_schema").Sets[0].Rows) || len(query("PRAGMA table_info(native_query_only_schema)").Sets[0].Rows) != 2 {
		t.Fatal("rejected schema write changed the original table")
	}
	if err := tt.Click("原始结构回执"); err != nil {
		t.Fatal(err)
	}
	rqliteSchemaPublicReceipt(t, tt, s.objectWorkspace, out, "query-only-rejected")
	out.Receipt.NativeJSON = "caller mutation"
	if d.SchemaDesignOriginalOutcome().Receipt.NativeJSON != native.Receipt.NativeJSON {
		t.Fatal("caller changed original query-only evidence")
	}
	s.objectWorkspace.Close(d)
	if d.SchemaDesignOriginalOutcome() != nil {
		t.Fatal("closed query-only designer retained receipt cache")
	}
}
