package shell

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func TestRQLiteNativePublicSchemaClearMenuOriginalReceiptAndWholeDatabaseRefresh(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite public clear fixtures")
	}
	for _, mode := range []string{"keep-identity", "reset-identity", "verification-rejected"} {
		t.Run(mode, func(t *testing.T) {
			parsed, err := connections.ParseURI(rqlitePublicFixture(t, "none"))
			if err != nil {
				t.Fatal(err)
			}
			parsed.Profile.ID = 889
			verifier, err := rqlite.OpenSession(context.Background(), parsed.Profile, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = verifier.Close() })
			query := rqliteSchemaPublicQuery(t, verifier)
			query("CREATE TABLE native_public_clear(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO native_public_clear VALUES(42,X'00FF0100'),(99,X'FE00');CREATE INDEX native_public_clear_index ON native_public_clear(v);CREATE TABLE clear_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO clear_audit VALUES(100,X'12');CREATE TABLE clear_child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES native_public_clear(id) ON DELETE CASCADE,v BLOB);INSERT INTO clear_child VALUES(1,42,X'33');CREATE TRIGGER native_public_clear_delete AFTER DELETE ON native_public_clear BEGIN INSERT INTO clear_audit(v) VALUES(old.v); END")
			catalog := query("SELECT type,name,tbl_name,rootpage,sql FROM sqlite_schema ORDER BY type,name").Sets[0].Rows
			fault := mode == "verification-rejected"
			s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "", fault)
			s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
			waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			if !s.objectWorkspace.CanClearTable(p.ID, "main", "native_public_clear") {
				t.Fatal("public clear capability missing")
			}
			docs := map[string]*dataworkspace.Document{}
			for _, name := range []string{"native_public_clear", "clear_audit", "clear_child"} {
				s.openMySQLData(*services, "main", name)
				d := s.currentTab().data
				docs[name] = d
				waitRQLiteSchemaPublic(t, tt, func() bool { return !d.Loading() })
			}
			s.openMySQLStructure(*services, "main", "native_public_clear")
			structure := s.currentTab().object
			waitRQLiteSchemaPublic(t, tt, func() bool { return !structure.Loading() })
			s.openQuery()
			sql := s.currentTab().query
			sql.Editor.Buffer.Set("SELECT 'retain original SQL'")
			activeID, activeIndex := s.currentTab().id, s.activeTab
			s.activeTab = 0
			s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: p.ID, Database: "main", Category: "表", Object: "native_public_clear"})
			tt.Frame()
			if err := tt.RightClick("native_public_clear"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("清空表"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if err := tt.Click("重新读取结构"); err != nil {
				t.Fatal(err)
			}
			workspace := s.objectWorkspace
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			if workspace.SchemaTableOriginalOutcome("clear") != nil || query("SELECT count(*) FROM native_public_clear").Sets[0].Rows[0][0].Text != "2" {
				t.Fatal("public clear preview executed")
			}
			reset := mode == "reset-identity"
			if reset {
				if err := tt.Click("重置自增值"); err != nil {
					t.Fatal(err)
				}
			}
			setTestInput(t, tt, "确认原表名称", "native_public_clear")
			s.activeTab = activeIndex
			tt.Frame()
			generation := s.runtime.Generation(p.ID)
			if err := tt.Click("确认清空表"); err != nil {
				t.Fatal(err)
			}
			s.activeTab, s.selectedProfileID = 0, p.ID+10000
			s.sessions.Activate(p.ID + 10000)
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(20 * time.Second):
				t.Fatal("native clear did not acknowledge")
			}
			if native.State != "committed" || !native.Verified || native.Receipt == nil {
				t.Fatal("original native clear failed", native.State, native.Error)
			}
			close(release)
			for range 5 {
				tt.Frame()
			}
			if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("clear") != nil {
				t.Fatal("another profile consumed original clear")
			}
			s.activeTab, s.selectedProfileID = activeIndex, p.ID
			s.sessions.Activate(p.ID)
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			out := workspace.SchemaTableOriginalOutcome("clear")
			if out == nil || out.Receipt == nil || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Sequence != 1 || out.Receipt.ResetIdentity != reset || out.Receipt.Original.Target.Generation != generation || out.Receipt.Operation != "clear" || out.Rows != 2 || out.Verified == fault || !workspace.TableDialogOpen() {
				t.Fatal("public clear lost original policy or complete native receipt", out)
			}
			if s.currentTab().id != activeID || s.currentTab().query != sql || sql.Editor.Buffer.String() != "SELECT 'retain original SQL'" {
				t.Fatal("clear changed unrelated active SQL")
			}
			if !reflect.DeepEqual(catalog, query("SELECT type,name,tbl_name,rootpage,sql FROM sqlite_schema ORDER BY type,name").Sets[0].Rows) {
				t.Fatal("clear changed original complete schema")
			}
			if query("SELECT count(*) FROM clear_child").Sets[0].Rows[0][0].Text != "0" || query("SELECT count(*) FROM clear_audit").Sets[0].Rows[0][0].Text != "3" {
				t.Fatal("clear suppressed declared trigger/cascade effects")
			}
			foundStructure := false
			for _, tab := range s.tabs {
				foundStructure = foundStructure || tab.object == structure
			}
			if foundStructure == fault {
				t.Fatal("clear did not retain original structure or revoke uncertain scope")
			}
			if fault {
				if out.Error == nil || !out.Receipt.Verified || s.runtime.IsConnected(p.ID) || workspace.CanClearTable(p.ID, "main", "native_public_clear") {
					t.Fatal("unverified native clear lost evidence or allowed replay")
				}
			} else {
				// Original inactive data completions remain parked until each tab is
				// activated. Every possible declared effect is refreshed.
				for name, d := range docs {
					found := false
					for i, tab := range s.tabs {
						if tab.data == d {
							s.activeTab = i
							found = true
							break
						}
					}
					if !found {
						t.Fatal("clear disposed original data document", name)
					}
					waitRQLiteSchemaPublic(t, tt, func() bool { return !d.Loading() })
					expected := 0
					if name == "clear_audit" {
						expected = 3
					}
					if d.Page() == nil || len(d.Page().Rows) != expected {
						t.Fatal("clear kept stale original database data", name, d.Page())
					}
				}
				s.activeTab = activeIndex
			}
			rqliteSchemaPublicReceipt(t, tt, workspace, out, "clear-"+mode)
			out.Receipt.NativeJSON = "caller mutation"
			if workspace.SchemaTableOriginalOutcome("clear").Receipt.NativeJSON != native.Receipt.NativeJSON {
				t.Fatal("caller rewrote original clear evidence")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("clear") != nil {
				t.Fatal("explicit clear close retained caches")
			}
			if reset {
				query("INSERT INTO native_public_clear(v) VALUES(X'44')")
				if query("SELECT id FROM native_public_clear").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("public reset choice did not reset native identity")
				}
			}
		})
	}
}

func TestRQLiteNativePublicSchemaClearGuardsTriggerTargetDraftAndPreservesForeignGeneration(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite database clear guards")
	}
	for _, mode := range []string{"discard", "save"} {
		t.Run(mode, func(t *testing.T) {
			parsed, err := connections.ParseURI(rqlitePublicFixture(t, "none"))
			if err != nil {
				t.Fatal(err)
			}
			parsed.Profile.ID = 889
			verifier, err := rqlite.OpenSession(context.Background(), parsed.Profile, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = verifier.Close() })
			query := rqliteSchemaPublicQuery(t, verifier)
			query("CREATE TABLE clear_guard_source(id INTEGER PRIMARY KEY,v TEXT);INSERT INTO clear_guard_source VALUES(1,'original');CREATE TABLE clear_guard_target(id INTEGER PRIMARY KEY,v TEXT);INSERT INTO clear_guard_target VALUES(1,'target original');CREATE TABLE clear_guard_observed(v TEXT);CREATE TRIGGER clear_guard_delete AFTER DELETE ON clear_guard_source BEGIN INSERT INTO clear_guard_observed SELECT v FROM clear_guard_target;UPDATE clear_guard_target SET v=old.v; END")
			s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "")
			s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
			waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			s.openMySQLData(*services, "main", "clear_guard_target")
			d := s.currentTab().data
			waitRQLiteSchemaPublic(t, tt, func() bool { return !d.Loading() })
			if err := tt.Click("批量编辑"); err != nil {
				t.Fatal(err)
			}
			s.dataWorkspace.EditCell(*services, d, 0, "v", contract.Cell{Kind: contract.Text, Text: "original guarded draft"})
			if !d.HasDraft() {
				t.Fatalf("trigger target could not stage original draft: busy=%t error=%s page=%+v", d.Busy(), d.Error(), d.Page())
			}
			draftID := s.currentTab().id
			workspace := s.objectWorkspace
			workspace.BeginTableChange(p.ID, "main", "clear_guard_source", "delete_rows")
			tt.Frame()
			if err := tt.Click("重新读取结构"); err != nil {
				t.Fatal(err)
			}
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			if !d.HasDraft() {
				t.Fatal("schema preview discarded trigger-target draft")
			}
			setTestInput(t, tt, "确认原表名称", "clear_guard_source")
			if !d.HasDraft() {
				t.Fatal("confirmation input discarded trigger-target draft")
			}
			if err := tt.Click("确认清空表"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !d.LeavePending() || !d.HasDraft() || workspace.TablePending() || query("SELECT count(*) FROM clear_guard_source").Sets[0].Rows[0][0].Text != "1" {
				t.Fatalf("clear database guard: leave=%t draft=%t pending=%t error=%s", d.LeavePending(), d.HasDraft(), workspace.TablePending(), workspace.TableError())
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !d.HasDraft() || workspace.TablePending() || s.currentTab().id != draftID {
				t.Fatal("cancelled database guard lost draft or changed document")
			}
			if err := tt.Click("确认清空表"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			choice := "放弃"
			if mode == "save" {
				choice = "应用更改"
			}
			if err := tt.Click(choice); err != nil {
				t.Fatal(err)
			}
			waitRQLiteSchemaPublic(t, tt, func() bool { return workspace.TablePending() })
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(20 * time.Second):
				t.Fatal("settled database guard did not dispatch original clear")
			}
			close(release)
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() && !d.Loading() })
			if !native.Verified || d.HasDraft() || d.Page().Rows[0][1].Text != "original" {
				t.Fatal("clear did not refresh trigger target after its draft decision", native.Error, d.Page())
			}
			expected := "target original"
			if mode == "save" {
				expected = "original guarded draft"
			}
			if query("SELECT v FROM clear_guard_observed").Sets[0].Rows[0][0].Text != expected {
				t.Fatal("clear ran before the original database draft decision settled")
			}
			// A stale publication or contextual generation cannot close a replacement
			// session, consume a draft, or change its current active document.
			s.dataWorkspace.EditCell(*services, d, 0, "v", contract.Cell{Kind: contract.Text, Text: "replacement retained draft"})
			target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(p.ID, "main").Object("table", "clear_guard_source"), Generation: s.runtime.Generation(p.ID)}
			event := objectworkspace.SchemaClearEvent{Original: target, Generation: s.sessions.Current().Generation, Database: "main", Outcome: contract.SchemaDesignOutcome{State: "uncertain"}}
			event.Original.Generation++
			s.schemaTableCleared(*services, event)
			event.Original = target
			event.Generation++
			s.schemaTableCleared(*services, event)
			if !s.runtime.IsConnected(p.ID) || !d.HasDraft() || s.currentTab().id != draftID {
				t.Fatal("foreign clear receipt changed replacement scope")
			}
		})
	}
}
