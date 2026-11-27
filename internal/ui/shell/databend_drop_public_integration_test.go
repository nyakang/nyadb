package shell

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
)

func TestDatabendNativePublicSchemaDropConfirmationReceiptAndParkedCleanup(t *testing.T) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	s, tt, p, host := databendSchemaPublicState(t, f)
	services := *host
	name := `public_drop_%猫"original`
	qualified := schemaddl.Quote(f.Database) + "." + schemaddl.Quote(name)
	query := func(sql string) [][]string { return databendWorkbenchSQL(t, wf, sql) }
	query("CREATE TABLE " + qualified + "(id UInt64 NOT NULL, v Binary)")
	query("INSERT INTO " + qualified + " VALUES(18446744073709551615,unhex('00ff80'))")
	before := query("SELECT id,hex(v) FROM " + qualified)
	data := databendWorkbenchData(t, s, tt, services, p, name)
	s.openMySQLStructure(services, f.Database, name)
	structure := s.currentTab().object
	databendWorkbenchWait(t, tt, func() bool { return !structure.Loading() })
	other := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	activeID := s.currentTab().id
	workspace := s.objectWorkspace
	if !workspace.CanDropTable(p.ID, f.Database, name) || !workspace.CanClearTable(p.ID, f.Database, name) || !workspace.CanCopyTable(p.ID, f.Database, name) || workspace.CanCreateTable(p.ID, f.Database) {
		t.Fatal("public DROP capability publication differs")
	}
	openPreview := func() {
		workspace.BeginTableChange(p.ID, f.Database, name, mysqlsvc.MySQLTableDrop)
		tt.Frame()
		if !workspace.TableDialogOpen() {
			t.Fatal("DROP modal missing")
		}
		databendWorkbenchClick(t, tt, "重新读取结构")
		databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
		if workspace.TableError() != "" {
			t.Fatal("native drop snapshot", workspace.TableError())
		}
	}
	openPreview()
	setTestInput(t, tt, "确认原表名称", "different original name")
	databendWorkbenchClick(t, tt, "确认删除表")
	if workspace.TablePending() || workspace.SchemaTableOriginalOutcome("drop") != nil || !reflect.DeepEqual(before, query("SELECT id,hex(v) FROM "+qualified)) {
		t.Fatal("wrong name confirmation dispatched DROP")
	}
	setTestInput(t, tt, "确认原表名称", name)
	databendWorkbenchCapture(t, wf, tt, "drop-preview", "确认原表名称", "确认删除表", "取消")
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || !reflect.DeepEqual(before, query("SELECT id,hex(v) FROM "+qualified)) {
		t.Fatal("preview cancel changed original table")
	}
	openPreview()
	setTestInput(t, tt, "确认原表名称", name)
	generation := s.runtime.Generation(p.ID)
	databendWorkbenchClick(t, tt, "确认删除表")
	originalTab := s.activeTab
	s.activeTab = 0
	s.selectedProfileID = p.ID + 10000
	s.sessions.Activate(p.ID + 10000)
	deadline := time.Now().Add(60 * time.Second)
	for {
		rows := query("SELECT name FROM system.tables WHERE database='" + f.Database + "' AND name='" + name + "'")
		tt.Frame()
		if len(rows) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native DROP did not reach original database")
		}
	}
	if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("drop") != nil {
		t.Fatal("foreign active profile consumed DROP receipt")
	}
	s.activeTab = originalTab
	s.selectedProfileID = p.ID
	s.sessions.Activate(p.ID)
	databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
	out := workspace.SchemaTableOriginalOutcome("drop")
	if out == nil || out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || out.Actual.Schema.Exists || out.Receipt == nil || out.Receipt.Operation != "drop" || out.Receipt.Original.Target.Generation != generation || out.Actual.Target != out.Receipt.Original.Target {
		t.Fatal("public DROP lost original absent target", out)
	}
	if out.Receipt.Original.Target.Object != s.runtime.Namespace(p.ID, f.Database).Object("table", name) {
		t.Fatal("DROP original scope differs")
	}
	for _, tab := range s.tabs {
		if tab.data == data || tab.object == structure {
			t.Fatal("original scoped tabs survived DROP")
		}
	}
	if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(p.ID) {
		t.Fatal("DROP disturbed independent active tab or session")
	}
	databendWorkbenchCapture(t, wf, tt, "drop-receipt", "复制完整回执", "取消")
	databendWorkbenchClick(t, tt, "复制完整回执")
	var exported struct {
		Kind    string
		Receipt contract.SchemaDesignWriteReceipt
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &exported) != nil || exported.Kind != "schema-original-write-receipt" || !reflect.DeepEqual(exported.Receipt, *out.Receipt) {
		t.Fatal("complete original DROP clipboard receipt differs")
	}
	out.Receipt.NativeJSON = "caller mutation"
	if workspace.SchemaTableOriginalOutcome("drop").Receipt.NativeJSON != exported.Receipt.NativeJSON {
		t.Fatal("caller changed retained DROP evidence")
	}
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("drop") != nil {
		t.Fatal("closed DROP modal retained receipt")
	}
	databendWorkbenchCapture(t, wf, tt, "drop-complete")
	f.Journal(t, "public-drop.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "receipt": exported.Receipt, "before": before, "wrong_confirmation_rejected": true, "preview_cancelled": true, "parked_retained": true, "original_tabs_closed": true, "unrelated_tab_preserved": true, "processes": f.Starts.Load()})
}
