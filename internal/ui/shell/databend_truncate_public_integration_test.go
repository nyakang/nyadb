package shell

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func TestDatabendNativePublicSchemaTruncateConfirmationReceiptAndParkedRefresh(t *testing.T) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	s, tt, p, host := databendSchemaPublicState(t, f)
	services := *host
	name := `public_truncate_%猫"original`
	qualified := schemaddl.Quote(f.Database) + "." + schemaddl.Quote(name)
	query := func(sql string) [][]string { return databendWorkbenchSQL(t, wf, sql) }
	query("CREATE TABLE " + qualified + "(id UInt64 NOT NULL, v Binary)")
	query("INSERT INTO " + qualified + " VALUES(18446744073709551615,unhex('00ff80'))")
	before := query("SELECT id,hex(v) FROM " + qualified)
	// Keep the target outside the first navigator viewport even in a focused
	// run. The full matrix leaves enough earlier tables to virtualize this row.
	for i := range 40 {
		query("CREATE TABLE " + schemaddl.Quote(f.Database) + "." + schemaddl.Quote(fmt.Sprintf("menu_padding62_%03d", i)) + "(id UInt64)")
	}
	data := databendWorkbenchData(t, s, tt, services, p, name)
	s.openMySQLStructure(services, f.Database, name)
	structure := s.currentTab().object
	databendWorkbenchWait(t, tt, func() bool { return !structure.Loading() })
	other := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	activeID := s.currentTab().id
	workspace := s.objectWorkspace
	if !workspace.CanDropTable(p.ID, f.Database, name) || !workspace.CanTruncateTable(p.ID, f.Database, name) || !workspace.CanCopyTable(p.ID, f.Database, name) || workspace.CanCreateTable(p.ID, f.Database) {
		t.Fatal("public TRUNCATE capability publication differs")
	}
	openPreview := func() {
		t.Helper()
		node := navigation.Node{Kind: "object", ProfileID: p.ID, Database: f.Database, Category: "表", Object: name}
		s.revealNavigationNode(node)
		tt.Frame()
		row := s.navigation.SelectionRow(s.navigation.Roots())
		if row < 0 {
			t.Fatal("original table missing from navigator")
		}
		s.navigation.Tree.List.ScrollTo(row, ui.Center)
		tt.Frame()
		r, found := tt.Find(name)
		if !found || r.X >= 250 || r.Y < 100 || r.Y+r.H > 750 {
			t.Fatal("original navigator row is not visible", r)
		}
		// A same-named inactive data tab is also clickable. Use the visible
		// navigator label's left edge, not a clipped text center or the tab.
		tt.RightClickAt(r.X+min(r.W/2, 8), r.Y+r.H/2)
		if s.navigation.Key != node.Key() {
			t.Fatal("context menu changed original table identity")
		}
		if err := tt.ChooseMenuItem(locales.Text("截断表")); err != nil {
			t.Fatal("public truncate action", err)
		}
		tt.Frame()
		if !workspace.TableDialogOpen() {
			t.Fatal("TRUNCATE modal missing")
		}
		databendWorkbenchClick(t, tt, "重新读取结构")
		databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
		if workspace.TableError() != "" {
			t.Fatal("native clear snapshot", workspace.TableError())
		}
	}
	openPreview()
	if _, found := tt.Find(locales.Text("重置自增值")); found {
		t.Fatal("Databend clear exposed unsupported identity reset")
	}
	if s.runtime.SchemaDesign(p.ID).Truncate != nil {
		t.Fatal("session-level truncate executor exposed")
	}
	setTestInput(t, tt, "确认原表名称", "different original name")
	databendWorkbenchClick(t, tt, "确认截断表")
	if workspace.TablePending() || workspace.SchemaTableOriginalOutcome("truncate") != nil || !reflect.DeepEqual(before, query("SELECT id,hex(v) FROM "+qualified)) {
		t.Fatal("wrong name confirmation dispatched TRUNCATE")
	}
	setTestInput(t, tt, "确认原表名称", name)
	databendWorkbenchCapture(t, wf, tt, "truncate-preview", "确认原表名称", "确认截断表", "取消")
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || !reflect.DeepEqual(before, query("SELECT id,hex(v) FROM "+qualified)) {
		t.Fatal("preview cancel changed original table")
	}
	openPreview()
	setTestInput(t, tt, "确认原表名称", name)
	generation := s.runtime.Generation(p.ID)
	databendWorkbenchClick(t, tt, "确认截断表")
	originalTab := s.activeTab
	s.activeTab = 0
	s.selectedProfileID = p.ID + 10000
	s.sessions.Activate(p.ID + 10000)
	deadline := time.Now().Add(60 * time.Second)
	for {
		rows := query("SELECT count(*) FROM " + qualified)
		tt.Frame()
		if reflect.DeepEqual(rows, [][]string{{"0"}}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native TRUNCATE did not reach original database")
		}
	}
	if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("truncate") != nil {
		t.Fatal("foreign active profile consumed TRUNCATE receipt")
	}
	s.activeTab = originalTab
	s.selectedProfileID = p.ID
	s.sessions.Activate(p.ID)
	databendWorkbenchWait(t, tt, func() bool { return !workspace.TablePending() })
	out := workspace.SchemaTableOriginalOutcome("truncate")
	if out == nil || out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || !out.Actual.Schema.Exists || out.Rows != 0 || out.Receipt == nil || out.Receipt.Operation != "truncate" || out.Receipt.Original.Target.Generation != generation || out.Actual.Target != out.Receipt.Original.Target || out.Receipt.Rows != 0 || out.Receipt.ResetIdentity || out.Actual.Schema.Databend.TableID != out.Receipt.Original.Schema.Databend.TableID {
		t.Fatal("public TRUNCATE lost original retained target", out)
	}
	if out.Receipt.Original.Target.Object != s.runtime.Namespace(p.ID, f.Database).Object("table", name) {
		t.Fatal("TRUNCATE original scope differs")
	}
	sourceData, sourceStructure := false, false
	sourceIndex := -1
	for i, tab := range s.tabs {
		if tab.data == data {
			sourceData = true
			sourceIndex = i
		}
		sourceStructure = sourceStructure || tab.object == structure
	}
	if !sourceData || !sourceStructure {
		t.Fatal("clear disposed original source documents")
	}

	if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(p.ID) {
		t.Fatal("TRUNCATE disturbed independent active tab or session")
	}
	originalActiveIndex := s.activeTab
	s.activeTab = sourceIndex
	databendWorkbenchWait(t, tt, func() bool { return !data.Loading() })
	if data.Error() != "" || data.Page() == nil || len(data.Page().Rows) != 0 {
		t.Fatal("clear kept stale source grid", data.Error())
	}
	s.activeTab = originalActiveIndex
	databendWorkbenchWait(t, tt, func() bool { return !other.Loading() })
	if other.Error() != "" || other.Page() == nil || len(other.Page().Rows) != 1 {
		t.Fatal("clear changed unrelated grid", other.Error())
	}
	databendWorkbenchCapture(t, wf, tt, "truncate-receipt", "复制完整回执", "取消")
	databendWorkbenchClick(t, tt, "复制完整回执")
	var exported struct {
		Kind    string
		Receipt contract.SchemaDesignWriteReceipt
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &exported) != nil || exported.Kind != "schema-original-write-receipt" || !reflect.DeepEqual(exported.Receipt, *out.Receipt) {
		t.Fatal("complete original TRUNCATE clipboard receipt differs")
	}
	out.Receipt.NativeJSON = "caller mutation"
	if workspace.SchemaTableOriginalOutcome("truncate").Receipt.NativeJSON != exported.Receipt.NativeJSON {
		t.Fatal("caller changed retained TRUNCATE evidence")
	}
	databendWorkbenchClick(t, tt, "取消")
	if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("truncate") != nil {
		t.Fatal("closed TRUNCATE modal retained receipt")
	}
	databendWorkbenchCapture(t, wf, tt, "truncate-complete")
	f.Journal(t, "public-truncate.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "receipt": exported.Receipt, "before": before, "public_context_menu": true, "wrong_confirmation_rejected": true, "preview_cancelled": true, "parked_retained": true, "source_tabs_preserved": true, "source_grid_refreshed": true, "unrelated_tab_preserved": true, "processes": f.Starts.Load()})
}
