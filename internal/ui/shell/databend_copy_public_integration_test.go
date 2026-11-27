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
	"github.com/nyakang/nyadb/locales"
)

func TestDatabendNativePublicSchemaCopyDataPreviewReceiptAndParkedCompletion(t *testing.T) {
	databendPublicCopy(t, true)
}
func TestDatabendNativePublicSchemaCopyStructurePreviewReceiptAndParkedCompletion(t *testing.T) {
	databendPublicCopy(t, false)
}
func databendPublicCopy(t *testing.T, withData bool) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	s, tt, p, host := databendSchemaPublicState(t, f)
	services := *host
	name, destination := `public_copy_%猫"original`, `public_copy_%猫"copied`
	if !withData {
		name += "_empty"
		destination += "_empty"
	}
	qualified := func(name string) string { return schemaddl.Quote(f.Database) + "." + schemaddl.Quote(name) }
	query := func(sql string) [][]string { return databendWorkbenchSQL(t, wf, sql) }
	query("CREATE TABLE " + qualified(name) + " (id UInt64 NOT NULL,v Binary,txt String DEFAULT 'retained')")
	query("INSERT INTO " + qualified(name) + " (id,v) VALUES (18446744073709551615,unhex('00ff80')),(18446744073709551615,unhex('00ff80'))")
	before := query("SELECT id,hex(v),txt FROM " + qualified(name))
	data := databendWorkbenchData(t, s, tt, services, p, name)
	s.openMySQLStructure(services, f.Database, name)
	structure := s.currentTab().object
	databendWorkbenchWait(t, tt, func() bool { return !structure.Loading() })
	other := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	activeID := s.currentTab().id
	workspace := s.objectWorkspace
	if !workspace.CanRenameTable(p.ID, f.Database, name) || !workspace.CanDropTable(p.ID, f.Database, name) || workspace.CanClearTable(p.ID, f.Database, name) || !workspace.CanCopyTable(p.ID, f.Database, name) || workspace.CanCreateTable(p.ID, f.Database) || workspace.CanChangeTable(p.ID) {
		t.Fatal("public object capability exposure differs from implemented rename/drop/copy")
	}
	if ops := s.runtime.SchemaDesign(p.ID); ops.Rename != nil || ops.Apply != nil || ops.Drop != nil || ops.Copy != nil {
		t.Fatal("session-level schema executor exposed")
	}
	openPreview := func() {
		action := mysqlsvc.MySQLTableCopyData
		if !withData {
			action = mysqlsvc.MySQLTableCopyStructure
		}
		workspace.BeginTableChange(p.ID, f.Database, name, action)
		tt.Frame()
		if !workspace.TableDialogOpen() {
			t.Fatal("public copy modal missing")
		}
		databendWorkbenchClick(t, tt, "重新读取结构")
		databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
		if workspace.TableError() != "" {
			t.Fatal("native original snapshot", workspace.TableError())
		}
		for _, label := range []string{"自引用外键绑定到新表", "保留隐藏 rowid"} {
			if _, found := tt.Find(locales.Text(label)); found {
				t.Fatal("Databend exposed unsupported copy flag", label)
			}
		}
		setTestInput(t, tt, "目标表名称", destination)
		tt.Frame()
	}
	openPreview()
	databendWorkbenchCapture(t, wf, tt, "copy-preview", "目标表名称", "确认复制表", "取消")
	databendWorkbenchClick(t, tt, "取消")
	cancelledTargets := query("SELECT name FROM system.tables WHERE database='" + f.Database + "' AND name='" + destination + "'")
	if workspace.TableDialogOpen() || len(cancelledTargets) != 0 || !reflect.DeepEqual(before, query("SELECT id,hex(v),txt FROM "+qualified(name))) {
		t.Fatal("preview cancellation copied or changed original rows")
	}
	openPreview()
	originalGeneration := s.runtime.Generation(p.ID)
	databendWorkbenchClick(t, tt, "确认复制表")
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
			t.Fatal("native copy did not reach original database")
		}
	}
	if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("copy") != nil {
		t.Fatal("another profile consumed parked original acknowledgement")
	}
	s.activeTab = originalTab
	s.selectedProfileID = p.ID
	s.sessions.Activate(p.ID)
	databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
	out := workspace.SchemaTableOriginalOutcome("copy")
	if out == nil || out.State != "committed" || !out.Verified || out.Error != nil || out.Receipt == nil || out.Actual == nil {
		t.Fatal("public copy failed", out, workspace.TableError())
	}
	if out.Receipt.Original.Target.Generation != originalGeneration || out.Receipt.Original.Target.Object != s.runtime.Namespace(p.ID, f.Database).Object("table", name) || out.Receipt.Destination != destination || out.Actual.Schema.Databend.TableID == out.Receipt.Original.Schema.Databend.TableID || out.Receipt.WithData != withData || out.Receipt.RebindSelfReferences || out.Receipt.PreserveRowID {
		t.Fatal("public generation or original identity changed")
	}
	after := query("SELECT id,hex(v),txt FROM " + qualified(destination))
	if withData && !reflect.DeepEqual(before, after) || !withData && len(after) != 0 || !reflect.DeepEqual(before, query("SELECT id,hex(v),txt FROM "+qualified(name))) {
		t.Fatal("public copy changed exact native rows", before, after)
	}
	sourceData, sourceStructure := false, false
	for _, tab := range s.tabs {
		sourceData = sourceData || tab.data == data
		sourceStructure = sourceStructure || tab.object == structure
	}
	if !sourceData || !sourceStructure {
		t.Fatal("copy disposed original source documents")
	}

	if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(p.ID) {
		t.Fatal("copy disturbed unrelated active document or session")
	}
	databendWorkbenchCapture(t, wf, tt, "copy-receipt", "复制完整回执", "取消")
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
	if workspace.SchemaTableOriginalOutcome("copy").Receipt.NativeJSON != receipt.NativeJSON {
		t.Fatal("caller rewrote original receipt")
	}
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("copy") != nil {
		t.Fatal("terminal close retained modal/receipt")
	}
	databendWorkbenchCapture(t, wf, tt, "copy-complete")
	f.Journal(t, "public-copy.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "receipt": receipt, "before": before, "after": after, "preview_cancelled": true, "parked_retained": true, "source_tabs_preserved": true, "with_data": withData, "unrelated_tab_preserved": true, "processes": f.Starts.Load()})
}
