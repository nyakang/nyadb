package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestRQLiteNativePublicSchemaRebuildGuardsOriginalDatabaseAndParksReceipt(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires official RQLite public reconstruction fixtures")
	}
	for _, fixture := range []struct {
		name, choice string
		fk, cycle    bool
	}{
		{"discard", "discard", true, false}, {"save", "save", true, false},
		{"fk-off-discard", "discard", false, false}, {"fk-off-save", "save", false, false},
		{"cycle-discard", "discard", true, true}, {"cycle-save", "save", true, true},
		{"baseline-fk-on-discard", "discard", true, false}, {"baseline-fk-on-save", "save", true, false},
		{"baseline-fk-off-discard", "discard", false, false}, {"baseline-fk-off-save", "save", false, false},
		{"scope-fk-on-discard", "discard", true, false}, {"scope-fk-on-save", "save", true, false},
		{"scope-fk-off-discard", "discard", false, false}, {"scope-fk-off-save", "save", false, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			choice := fixture.choice
			parsed, err := connections.ParseURI(rqlitePublicFixture(t, "none", fixture.fk))
			if err != nil {
				t.Fatal(err)
			}
			parsed.Profile.ID = 889
			parsed.Profile.RQLiteOptions.BatchTransaction = fixture.cycle
			verifier, err := rqlite.OpenSession(context.Background(), parsed.Profile, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = verifier.Close() })
			query := rqliteSchemaPublicQuery(t, verifier)
			if fixture.cycle {
				query("CREATE TABLE rebuild_source(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER NOT NULL DEFAULT 7 REFERENCES rebuild_child(id) ON DELETE SET NULL,v BLOB);CREATE TABLE rebuild_child(id INTEGER PRIMARY KEY,parent INTEGER NOT NULL REFERENCES rebuild_source(id) ON DELETE SET NULL,v TEXT);PRAGMA defer_foreign_keys=ON;INSERT INTO rebuild_source(id,parent,v) VALUES(42,7,X'00FF');INSERT INTO rebuild_child VALUES(7,42,'original child');UPDATE sqlite_sequence SET seq=500 WHERE name='rebuild_source';CREATE VIEW rebuild_view AS SELECT id,v FROM rebuild_source")
			} else {
				query("CREATE TABLE rebuild_source(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO rebuild_source VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='rebuild_source';CREATE TABLE rebuild_child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES rebuild_source(id) ON DELETE CASCADE,v TEXT);INSERT INTO rebuild_child VALUES(7,42,'original child');CREATE VIEW rebuild_view AS SELECT id,v FROM rebuild_source")
			}
			if strings.HasPrefix(fixture.name, "baseline-") || strings.HasPrefix(fixture.name, "scope-") {
				config, err := connections.RQLiteProfileConfig(parsed.Profile)
				if err != nil {
					t.Fatal(err)
				}
				setup := []string{"PRAGMA foreign_keys=OFF"}
				if strings.HasPrefix(fixture.name, "scope-") {
					setup = append(setup, "CREATE TABLE rebuild_unrelated(rowid TEXT,oid TEXT,_rowid_ TEXT,v BLOB,parent INTEGER REFERENCES absent_parent(id))", "INSERT INTO rebuild_unrelated VALUES('r','o','_r',X'610080FF',99)")
				} else {
					setup = append(setup, "UPDATE rebuild_child SET parent=99")
				}
				setup = append(setup, fmt.Sprintf("PRAGMA foreign_keys=%d", map[bool]int{false: 0, true: 1}[fixture.fk]))
				body, err := json.Marshal(setup)
				if err != nil {
					t.Fatal(err)
				}
				client := &http.Client{Timeout: 10 * time.Second}
				response, err := client.Post(config.Endpoint+"/db/execute", "application/json", strings.NewReader(string(body)))
				if err != nil {
					t.Fatal(err)
				}
				raw, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				var decoded struct {
					Results []struct {
						Error   string          `json:"error"`
						ErrorV2 json.RawMessage `json:"error_v2"`
					} `json:"results"`
				}
				if readErr != nil || response.StatusCode != http.StatusOK || json.Unmarshal(raw, &decoded) != nil || len(decoded.Results) != len(setup) {
					t.Fatal("original native public baseline setup incomplete", readErr)
				}
				for _, result := range decoded.Results {
					if result.Error != "" || len(result.ErrorV2) != 0 && string(result.ErrorV2) != "null" {
						t.Fatal("original native public baseline setup rejected")
					}
				}
				if query("SELECT count(*) FROM pragma_foreign_key_check").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("original public violation baseline missing")
				}
				if folder := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); folder != "" {
					if err := os.MkdirAll(folder, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(folder, "schema-public-rebuild-"+fixture.name+"-baseline-setup.json"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := query("SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows
			s, tt, services, p, observed, release := rqliteSchemaPublicShell(t, parsed.Profile, "")
			s.submitNavigation(*services, navigation.DatabaseNode(p.ID, "main"))
			waitRQLiteSchemaPublic(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			s.openMySQLData(*services, "main", "rebuild_child")
			child := s.currentTab().data
			waitRQLiteSchemaPublic(t, tt, func() bool { return !child.Loading() })
			if err := tt.Click("批量编辑"); err != nil {
				t.Fatal(err)
			}
			s.dataWorkspace.EditCell(*services, child, 0, "v", contract.Cell{Kind: contract.Text, Text: "original guarded child draft"})
			if !child.HasDraft() {
				t.Fatal("original sibling data draft was not retained")
			}
			s.openMySQLStructure(*services, "main", "rebuild_source", true)
			design := s.currentTab().object
			designID := s.currentTab().id
			waitRQLiteSchemaPublic(t, tt, func() bool { return !design.DesignBusy() && tt.HasText("添加字段") })
			if err := tt.Click("约束"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("添加约束"); err != nil {
				t.Fatal(err)
			}
			field, ok := tt.AccessByRole(ui.RoleTextField, "检查表达式")
			if !ok || field.ReadOnly || field.Disabled || field.Offscreen {
				t.Fatal("the public constraint expression input was not available")
			}
			tt.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
			tt.Command("selectAll")
			tt.Type("id>0")
			tt.Frame()
			if field, ok := tt.AccessByRole(ui.RoleTextField, "检查表达式"); !ok || field.Value != "id>0" {
				t.Fatal("the original public expression input did not receive the draft")
			}
			if err := tt.Click("应用定义修改"); err != nil {
				t.Fatal(err)
			}
			version := query("PRAGMA schema_version").Sets[0].Rows
			if err := s.objectWorkspace.PrepareSchemaDesign(design); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !child.HasDraft() || !reflect.DeepEqual(version, query("PRAGMA schema_version").Sets[0].Rows) || design.SchemaDesignOriginalOutcome() != nil {
				t.Fatal("reconstruction preview wrote data or discarded a sibling draft")
			}
			if err := tt.Click("确认执行结构 SQL"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !child.LeavePending() || !child.HasDraft() || design.DesignBusy() {
				t.Fatal("reconstruction did not guard the original database drafts")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !child.HasDraft() || !design.DesignDirty() {
				t.Fatal("cancelled reconstruction guard lost an original draft")
			}
			// The guard temporarily activates its owning data document.
			for i, tab := range s.tabs {
				if tab.id == designID {
					s.activeTab = i
				}
			}
			s.activateMySQLForCurrentTab()
			tt.Frame()
			if err := tt.Click("确认执行结构 SQL"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			button := "放弃"
			if choice == "save" {
				button = "应用更改"
			}
			if err := tt.Click(button); err != nil {
				t.Fatal(err)
			}
			waitRQLiteSchemaPublic(t, tt, func() bool { return design.DesignBusy() })
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(25 * time.Second):
				t.Fatal("settled database guard did not dispatch original reconstruction")
			}
			constraint := native.Receipt != nil && native.Receipt.Desired != nil && strings.Contains(strings.ReplaceAll(native.Receipt.Desired.Clauses[len(native.Receipt.Desired.Clauses)-1].SQL, " ", ""), "CHECK(id>0)")
			if !constraint || !native.Verified || native.State != "committed" || native.Receipt == nil || !strings.Contains(strings.Join(native.Receipt.Statements, "\n"), "__nyadb_rebuild_") {
				t.Fatal("public reconstruction did not retain a verified original receipt", native.Error)
			}
			for i, tab := range s.tabs {
				if tab.data == child {
					s.activeTab = i
				}
			}
			s.activateMySQLForCurrentTab()
			if s.currentTab().object == design {
				t.Fatal("the original reconstruction document was not parked")
			}
			close(release)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}
			if !design.DesignBusy() || design.SchemaDesignOriginalOutcome() != nil {
				t.Fatal("parked reconstruction receipt was consumed by the sibling document")
			}
			for i, tab := range s.tabs {
				if tab.id == designID {
					s.activeTab = i
				}
			}
			s.activateMySQLForCurrentTab()
			waitRQLiteSchemaPublic(t, tt, func() bool { return !design.DesignBusy() })
			out := design.SchemaDesignOriginalOutcome()
			if out == nil || !out.Verified || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Original.Target.Object.ProfileID != p.ID || out.Receipt.Original.Target.Object.Catalog != "main" || out.Receipt.Original.Target.Object.Schema != "" || child.HasDraft() || design.DesignDirty() {
				t.Fatal("public original reconstruction receipt or identity was not retained")
			}
			expected := "original child"
			if choice == "save" {
				expected = "original guarded child draft"
			}
			if query("SELECT v FROM rebuild_child").Sets[0].Rows[0][0].Text != expected || !reflect.DeepEqual(before, query("SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows) {
				t.Fatal("reconstruction changed saved/discarded child values or original sequence")
			}
			provider, err := contract.Require[contract.QueryProvider](verifier)
			if err != nil {
				t.Fatal(err)
			}
			check, err := provider.ReserveQuery(890)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(check.Release)
			rejected, rejectErr := check.Execute(context.Background(), "main", "INSERT INTO rebuild_source(id,v) VALUES(-1,X'00AA')")
			if rejectErr == nil || !strings.Contains(rejected.NativeJSON, "CHECK constraint failed") || query("SELECT count(*) FROM rebuild_source WHERE id=-1").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("the original public CHECK expression was not enforced", rejectErr)
			}
			if !reflect.DeepEqual(before, query("SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows) {
				t.Fatal("rejected constraint write changed the original sequence")
			}
			if folder := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); folder != "" {
				if err := os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				proof, err := json.Marshal(struct {
					ExpectedExpression, RejectedNative, RejectedError, ChildValue string
					OriginalSequence                                              [][]contract.Cell
				}{"id>0", rejected.NativeJSON, rejectErr.Error(), expected, before})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(folder, "schema-public-rebuild-"+fixture.name+"-constraint-proof.json"), proof, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(fixture.name, "baseline-") {
				if query("SELECT count(*) FROM pragma_foreign_key_check").Sets[0].Rows[0][0].Text != "1" || query("SELECT parent FROM rebuild_child WHERE id=7").Sets[0].Rows[0][0].Text != "99" {
					t.Fatal("public rebuilding or later native CHECK rejection changed the original violation")
				}
			}
			if strings.HasPrefix(fixture.name, "scope-") {
				unchanged := query("SELECT rowid,oid,_rowid_,hex(v),parent FROM rebuild_unrelated").Sets[0].Rows
				if len(unchanged) != 1 || len(unchanged[0]) != 5 || unchanged[0][0].Text != "r" || unchanged[0][1].Text != "o" || unchanged[0][2].Text != "_r" || unchanged[0][3].Text != "610080FF" || unchanged[0][4].Text != "99" || query("SELECT count(*) FROM pragma_foreign_key_check").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("public scoped reconstruction changed original unrelated rows or missing-parent baseline")
				}
				for _, statement := range out.Receipt.Statements {
					if strings.Contains(statement, "main.\"rebuild_unrelated\"") {
						t.Fatal("public scoped reconstruction touched the original unrelated physical table", statement)
					}
				}
				if folder := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); folder != "" {
					proof, err := json.Marshal(unchanged)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(folder, "schema-public-rebuild-"+fixture.name+"-unrelated-proof.json"), proof, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tt.Click("原始结构回执"); err != nil {
				t.Fatal(err)
			}
			rqliteSchemaPublicReceipt(t, tt, s.objectWorkspace, out, "rebuild-"+fixture.name)
		})
	}
}
