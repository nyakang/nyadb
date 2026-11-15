package shell

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestRQLiteNativePublicSchemaCopyRetainsOriginalReceiptSourceTabsAndUnknownWriteProtection(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite public copy fixtures")
	}
	for _, mode := range []string{"data", "structure", "verification-rejected"} {
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
			query("CREATE TABLE native_public_copy_source(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO native_public_copy_source VALUES(9007199254740993,X'00FF0100'),(9223372036854775807,X'FE00');CREATE INDEX native_public_copy_index ON native_public_copy_source(v);CREATE TABLE other(id INTEGER PRIMARY KEY)")
			before := query("SELECT id,v FROM native_public_copy_source ORDER BY id").Sets[0].Rows
			fault := mode == "verification-rejected"
			s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "", fault)
			s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
			waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			if !s.objectWorkspace.CanCopyTable(p.ID, "main", "native_public_copy_source") {
				t.Fatal("actual public product does not expose original-lease copy")
			}
			s.openMySQLData(*services, "main", "native_public_copy_source")
			data := s.currentTab().data
			waitRQLiteSchemaPublic(t, tt, func() bool { return !data.Loading() })
			s.openMySQLStructure(*services, "main", "native_public_copy_source")
			structure := s.currentTab().object
			waitRQLiteSchemaPublic(t, tt, func() bool { return !structure.Loading() })
			s.openQuery()
			other := s.currentTab().query
			other.Editor.Buffer.Set("SELECT 'retained original SQL'")
			activeID, activeIndex := s.currentTab().id, s.activeTab
			workspace := s.objectWorkspace
			action := mysqlsvc.MySQLTableCopyData
			if mode == "structure" {
				action = mysqlsvc.MySQLTableCopyStructure
			}
			workspace.BeginTableChange(p.ID, "main", "native_public_copy_source", action)
			tt.Frame()
			if err := tt.Click("重新读取结构"); err != nil {
				t.Fatal(err)
			}
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			setTestInput(t, tt, "目标表名称", "native_public_copied")
			if !reflect.DeepEqual(before, query("SELECT id,v FROM native_public_copy_source ORDER BY id").Sets[0].Rows) || workspace.SchemaTableOriginalOutcome("copy") != nil {
				t.Fatal("native copy preview mutated rows or fabricated a write receipt")
			}
			generation := s.runtime.Generation(p.ID)
			if err := tt.Click("确认复制表"); err != nil {
				t.Fatal(err)
			}
			s.activeTab, s.selectedProfileID = 0, p.ID+10000
			s.sessions.Activate(p.ID + 10000)
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(20 * time.Second):
				t.Fatal("actual native copy did not acknowledge")
			}
			if native.State != "committed" || !native.Verified || native.Receipt == nil {
				t.Fatal("native copy failed", native.State, native.Error)
			}
			close(release)
			for i := 0; i < 5; i++ {
				tt.Frame()
			}
			if !workspace.TablePending() || workspace.SchemaTableOriginalOutcome("copy") != nil {
				t.Fatal("another profile consumed original copy completion")
			}
			s.activeTab, s.selectedProfileID = activeIndex, p.ID
			s.sessions.Activate(p.ID)
			waitRQLiteSchemaPublic(t, tt, func() bool { return !workspace.TablePending() })
			out := workspace.SchemaTableOriginalOutcome("copy")
			if out == nil || out.Receipt == nil || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Sequence != 1 || out.Receipt.Original.Target.Generation != generation || out.Receipt.Original.Target.Object.Name != "native_public_copy_source" || out.Receipt.Destination != "native_public_copied" || out.Receipt.WithData != (mode != "structure") || out.Verified == fault || !workspace.TableDialogOpen() {
				t.Fatal("public runtime/shell lost the original complete copy receipt", out)
			}
			if s.currentTab().id != activeID || s.currentTab().query != other || other.Editor.Buffer.String() != "SELECT 'retained original SQL'" {
				t.Fatal("copy changed an unrelated active document")
			}
			foundData, foundStructure := false, false
			for _, tab := range s.tabs {
				foundData = foundData || tab.data == data
				foundStructure = foundStructure || tab.object == structure
			}
			if foundData == fault || foundStructure == fault || !reflect.DeepEqual(before, query("SELECT id,v FROM native_public_copy_source ORDER BY id").Sets[0].Rows) {
				t.Fatal("copy disposed or changed its original source")
			}
			copied := query("SELECT id,v FROM native_public_copied ORDER BY id").Sets[0].Rows
			if mode == "structure" && len(copied) != 0 || mode != "structure" && !reflect.DeepEqual(before, copied) {
				t.Fatal("copy ignored original complete data choice")
			}
			if fault {
				if out.Error == nil || !out.Receipt.Verified || s.runtime.IsConnected(p.ID) || workspace.CanCopyTable(p.ID, "main", "native_public_copy_source") {
					t.Fatal("host rejected verification without retaining original evidence and protection")
				}
			}
			rqliteSchemaPublicReceipt(t, tt, workspace, out, "copy-"+mode)
			out.Receipt.NativeJSON = "caller mutation"
			if workspace.SchemaTableOriginalOutcome("copy").Receipt.NativeJSON != native.Receipt.NativeJSON {
				t.Fatal("caller rewrote original copy evidence")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if workspace.TableDialogOpen() || workspace.SchemaTableOriginalOutcome("copy") != nil {
				t.Fatal("explicit copy close retained original caches")
			}
		})
	}
}
