package dataworkspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func parkNeo4jSave(t *testing.T, d *Document) mysqlEditResult {
	t.Helper()
	select {
	case result := <-d.edit.Results:
		out := make(chan mysqlEditResult, 1)
		out <- result
		d.edit.Results = out
		return result
	case <-time.After(30 * time.Second):
		t.Fatal("original native property save stalled")
	}
	return mysqlEditResult{}
}

func TestNeo4jNativePublicDataPropertyEditingTypedReceiptsParkConflictsAndContinuedEdits(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label, rel := "NyaGridEdit_"+f.Nonce, "NyaGridEditRel_"+f.Nonce
	neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1,long:$long,items:[1,2],date:date('2026-01-01')}),(b:$($label) {n:2,long:$long,items:[3],date:date('2026-01-01')}) CREATE (a)-[r:$($rel) {n:1,long:$long,items:[1],date:date('2026-01-01')}]->(b)", map[string]any{"label": label, "rel": rel, "long": strings.Repeat("original 猫 ", 900)})
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	profile := neo4jDataProfile(t, f)
	handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active := Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, RuntimeFor: manager.Data})
	defer s.CloseAll()
	for _, target := range []struct{ kind, name string }{{"label", label}, {"relationship", rel}} {
		t.Run(target.kind, func(t *testing.T) {
			object := manager.Namespace(profile.ID, "neo4j").Object(target.kind, target.name)
			d := s.OpenObject(1, object, handle.Capabilities())
			defer s.Close(d)
			sibling := s.OpenObject(2, object, handle.Capabilities())
			defer s.Close(sibling)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			s.Load(ui.Services{}, sibling, false)
			waitNeo4jData(t, s, sibling)
			if d.err != "" || !s.runtimeFor(d).NativeReceipts || !d.editable() || !d.allowsDataEdit("update") || !d.allowsDataEdit("delete") || !d.allowsDataEdit("insert") {
				t.Fatal("native property edit mode not bound", d.err)
			}
			n, long, items, date := slices.Index(d.page.Columns, "property.n"), slices.Index(d.page.Columns, "property.long"), slices.Index(d.page.Columns, "property.items"), slices.Index(d.page.Columns, "property.date")
			for col, c := range d.page.ColumnMetadata {
				if mysqlDataCanEdit(d, 0, col) != strings.HasPrefix(c.Name, "property.") {
					t.Fatal("derived graph column editable", c.Name)
				}
			}
			cached, _ := json.Marshal(sibling.page)
			d.edit.Batch = true
			s.beginMySQLCellEdit(ui.Services{}, d, 0, n, false)
			if d.edit.Editor == nil {
				t.Fatal("native scalar cell editor missing")
			}
			d.edit.Editor.Text = "9223372036854775807"
			s.finishMySQLCellEdit(d, true)
			s.beginMySQLCellEdit(ui.Services{}, d, 0, long, true)
			waitNeo4jData(t, s, d)
			if d.edit.Editor == nil || len(d.edit.Editor.Text) < 1024 {
				t.Fatal("complete graph editing read failed", d.edit.Err)
			}
			d.edit.Editor.Text = strings.Repeat("complete edited 猫 ", 800)
			captureNeo4jData(t, s, d, "write-"+target.kind+"-long-editor", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
			s.finishMySQLCellEdit(d, true)
			s.beginMySQLCellEdit(ui.Services{}, d, 0, items, false)
			d.edit.Editor.Text = `{"Kind":"list","Values":[{"Kind":"integer","Text":"-9223372036854775808"}]}`
			s.finishMySQLCellEdit(d, true)
			s.beginMySQLCellEdit(ui.Services{}, d, 0, date, false)
			d.edit.Editor.Text = `{"Kind":"date","Text":"10000-02-29"}`
			s.finishMySQLCellEdit(d, true)
			captureNeo4jData(t, s, d, "write-"+target.kind+"-drafts")
			s.saveMySQLData(ui.Services{}, d)
			if !d.edit.Busy {
				t.Fatal("native shared grid save not dispatched", d.edit.Err)
			}
			active.ProfileID = 99
			result := parkNeo4jSave(t, d)
			s.Consume(ui.Services{})
			if !d.edit.Busy || d.nativeReceipt.JSON != "" || len(d.edit.Drafts) != 1 {
				t.Fatal("parked native save consumed by another profile")
			}
			if result.Err != nil || result.Saved == nil || !result.Saved.CommitConfirmed {
				t.Fatal("real typed grid save failed", result.Err)
			}
			active.ProfileID = profile.ID
			waitNeo4jData(t, s, d)
			if d.edit.Err != "" || mysqlDataHasDraft(d) || d.nativeReceipt.JSON != result.Saved.NativeJSON {
				t.Fatal("confirmed native receipt not owned by original document", d.edit.Err)
			}
			unchanged, _ := json.Marshal(sibling.page)
			if string(cached) != string(unchanged) {
				t.Fatal("original sibling page changed after save")
			}
			d.nativeReceipt.Open = true
			captureNeo4jData(t, s, d, "write-"+target.kind+"-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
			d.nativeReceipt.Open = false
			path := filepath.Join(t.TempDir(), "receipt.json")
			s.exportNativeWriteReceipt(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { return path, nil })
			deadline := time.Now().Add(10 * time.Second)
			for d.nativeReceipt.Results != nil && time.Now().Before(deadline) {
				s.Consume(ui.Services{})
				time.Sleep(time.Millisecond)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != result.Saved.NativeJSON || d.nativeReceipt.Err != "" {
				t.Fatal("native receipt export lost original bytes", err, d.nativeReceipt.Err)
			}
			// Continued edits use newly signed native rows; sibling old snapshots stay stale.
			s.beginMySQLCellEdit(ui.Services{}, d, 0, n, false)
			d.edit.Editor.Text = "12"
			s.finishMySQLCellEdit(d, true)
			s.saveMySQLData(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if d.edit.Err != "" || d.page.Rows[0][n].Value.Text != "12" {
				t.Fatal("continued graph property save failed", d.edit.Err)
			}
			oldReceipt := d.nativeReceipt.JSON
			s.beginMySQLCellEdit(ui.Services{}, d, 0, n, false)
			d.edit.Editor.Text = "34"
			s.finishMySQLCellEdit(d, true)
			id := d.page.RowRefs[0].TypedValues[0].Text
			pattern := "(e)"
			if target.kind == "relationship" {
				pattern = "()-[e]->()"
			}
			neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id SET e.n=56", map[string]any{"id": id})
			s.saveMySQLData(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if d.edit.Err == "" || len(d.edit.Drafts) != 1 || d.edit.Uncertain != nil || !strings.Contains(d.nativeReceipt.JSON, `"RollbackConfirmed":true`) || oldReceipt == d.nativeReceipt.JSON {
				t.Fatal("graph conflict lost drafts or replaced them with unconfirmed values", d.edit.Err)
			}
			captureNeo4jData(t, s, d, "write-"+target.kind+"-conflict")
			s.discardMySQLData(d)
			s.Load(ui.Services{}, d, true)
			waitNeo4jData(t, s, d)
			s.beginMySQLCellEdit(ui.Services{}, d, 0, n, false)
			d.edit.Editor.Text = "78"
			s.finishMySQLCellEdit(d, true)
			previousPage := d.page
			s.saveMySQLData(ui.Services{}, d)
			late := parkNeo4jSave(t, d)
			if late.Err != nil || late.Saved == nil || !late.Saved.CommitConfirmed {
				t.Fatal("late original native save failed", late.Err)
			}
			active.Generation++
			waitNeo4jData(t, s, d)
			if d.page != previousPage || len(d.edit.Drafts) != 1 || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || d.nativeReceipt.JSON != late.Saved.NativeJSON {
				t.Fatal("old generation receipt replaced new grid or lost original evidence")
			}
			s.saveMySQLData(ui.Services{}, d)
			if d.edit.Busy {
				t.Fatal("generation mismatch permitted original SQL replay")
			}
			active.Generation--
			s.Close(d)
			if d.nativeReceipt.JSON != "" || d.nativeReceipt.Results != nil || d.page != nil {
				t.Fatal("graph close retained native write receipts")
			}
		})
	}
}
