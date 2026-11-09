package dataworkspace

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type gridExtendedListSample struct{ kind, expression, after string }

func gridExtendedListSamples() []gridExtendedListSample {
	return []gridExtendedListSample{
		{"date", `[date({year:10000,month:2,day:29}),date({year:-1,month:2,day:28})]`, `["2028-02-29","2029-01-01"]`},
		{"local_time", `[localtime('23:59:59.999999999'),localtime('00:00:00')]`, `["01:02:03.000000001","23:59:59"]`},
		{"local_datetime", `[localdatetime({year:10000,month:2,day:29,hour:23,minute:59,second:59,nanosecond:999999999}),localdatetime({year:-1,month:2,day:28})]`, `["2026-01-01T01:02:03.000000001","2028-02-29T00:00:00"]`},
		{"time", `[time({hour:1,minute:2,second:3,nanosecond:1,timezone:'+00:20:34'}),time({hour:23,minute:59,second:59,nanosecond:999999999,timezone:'-00:20:34'})]`, `[{"value":"01:02:03.000000001+00:20","offset_seconds":1235},{"value":"00:00:00Z","offset_seconds":0}]`},
		{"datetime", `[datetime('2026-07-03T01:02:03.123456789[Europe/Stockholm]'),datetime('1890-01-01T00:00:00[Europe/Paris]')]`, `[{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Offset","offset_seconds":7200},{"value":"2026-01-01T00:00:00Z","zone":"UTC","offset_seconds":0}]`},
		{"duration", `[duration({days:1}),duration({months:-2,days:3,nanoseconds:-1})]`, `[{"months":0,"days":0,"seconds":86400,"nanoseconds":0},{"months":1,"days":0,"seconds":1,"nanoseconds":1000000001}]`},
		{"point2d", `[point({x:1,y:2}),point({x:-1,y:2})]`, `[{"srid":4326,"x":181,"y":-90},{"srid":4326,"x":180,"y":0}]`},
		{"point3d", `[point({x:1,y:2,z:3}),point({x:-1,y:2,z:3})]`, `[{"srid":4979,"x":113.5,"y":22.3,"z":-0},{"srid":4979,"x":180,"y":90,"z":-10}]`},
	}
}

func gridExtendedListAssignments(target string) string {
	var clauses []string
	for _, sample := range gridExtendedListSamples() {
		clauses = append(clauses, target+".list_"+sample.kind+"="+sample.expression)
	}
	return " SET " + strings.Join(clauses, ",")
}

func gridExtendedListValue(t *testing.T, sample gridExtendedListSample) contract.GraphValue {
	t.Helper()
	v, err := contract.ParseGraphPropertyList(sample.kind, sample.after)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func assertGridExtendedLists(t *testing.T, entity contract.GraphValue) {
	t.Helper()
	var props []contract.GraphProperty
	if entity.Node != nil {
		props = entity.Node.Properties
	} else if entity.Relationship != nil {
		props = entity.Relationship.Properties
	} else {
		t.Fatal("missing complete native entity")
	}
	for _, sample := range gridExtendedListSamples() {
		index := slices.IndexFunc(props, func(p contract.GraphProperty) bool { return p.Name == "list_"+sample.kind })
		if index < 0 || !reflect.DeepEqual(props[index].Value, gridExtendedListValue(t, sample)) {
			t.Fatal("native list lost complete components", sample.kind, props)
		}
	}
}

func extendedListRow(t *testing.T, d *Document, id string) int {
	t.Helper()
	row := slices.IndexFunc(d.page.RowRefs, func(ref *contract.RowRef) bool {
		return ref != nil && len(ref.TypedValues) == 1 && ref.TypedValues[0].Text == id
	})
	if row < 0 {
		t.Fatal("original identity missing from refreshed page", id)
	}
	return row
}

func TestNeo4jNativePublicDataExtendedListsTypedTransactionsCreationConflictsAndOriginalLease(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	label, rel := "NyaGridExtendedLists_"+f.Nonce, "NyaGridExtendedListsType_"+f.Nonce
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	query := "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2})" + gridExtendedListAssignments("a") + gridExtendedListAssignments("b") + " CREATE (a)-[r:$($rel)]->(b) SET r=properties(a) RETURN a,b,r"
	seed := neo4jDataRun(t, admin, query, map[string]any{"label": label, "rel": rel})
	source, target := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
	for _, object := range []struct{ kind, name string }{{"label", label}, {"relationship", rel}} {
		t.Run(object.kind, func(t *testing.T) {
			d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object(object.kind, object.name), caps)
			defer s.Close(d)
			sibling := s.OpenObject(2, d.object, caps)
			defer s.Close(sibling)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			s.Load(ui.Services{}, sibling, false)
			waitNeo4jData(t, s, sibling)
			original, _ := json.Marshal(sibling.page)
			for _, sample := range gridExtendedListSamples() {
				col := slices.Index(d.page.Columns, "property.list_"+sample.kind)
				if col < 0 || !mysqlDataCanEdit(d, 0, col) {
					t.Fatal("extended list is not in sole editable grid", sample.kind)
				}
				s.beginMySQLCellEdit(ui.Services{}, d, 0, col, true)
				waitNeo4jData(t, s, d)
				if d.edit.Editor == nil {
					t.Fatal("typed list editor missing", sample.kind, d.edit.Err)
				}
				raw, _ := json.Marshal(gridExtendedListValue(t, sample))
				editor := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
				editor.Frame()
				if err := editor.Click(locales.Text("专用单元格编辑") + " property.list_" + sample.kind); err != nil {
					t.Fatal(err)
				}
				editor.Key(ui.Cmd, ui.KeyA)
				editor.Type(string(raw))
				captureNeo4jData(t, s, d, "extended-list-"+object.kind+"-"+sample.kind+"-editor", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
				if err := editor.Click(locales.Text("应用单元格编辑")); err != nil {
					t.Fatal(err)
				}
				if d.edit.Editor != nil || d.edit.Err != "" {
					t.Fatal("visible complete list editor failed", d.edit.Err)
				}
			}
			draft := mysqlDataDraftForRow(d, 0)
			if draft == nil || len(draft.Values) != 8 {
				t.Fatal("typed list edits were dropped", d.edit.Err)
			}
			// Both public validation payloads retain complete, original typed cells.
			if object.kind == "relationship" {
				reader := s.runtimeFor(d).RowReader
				row, err := reader(context.Background(), d.profileID, d.database, d.table, *d.page.RowRefs[0])
				if err != nil {
					t.Fatal(err)
				}
				draft.Original.Mode, draft.Original.Fingerprints = "values", nil
				for _, name := range draft.Original.Columns {
					value, ok := row.Values[name]
					if !ok {
						t.Fatal("full values payload incomplete", name)
					}
					draft.Original.Original = append(draft.Original.Original, contract.Cell{Kind: value.Kind, Text: value.Text, Bytes: slices.Clone(value.Bytes)})
				}
			}
			captureNeo4jData(t, s, d, "extended-list-"+object.kind+"-draft")
			s.Save(ui.Services{}, d)
			active.ProfileID = 99
			receipt := parkNeo4jSave(t, d)
			s.Consume(ui.Services{})
			if !d.edit.Busy || !d.HasDraft() || receipt.Err != nil || receipt.Saved == nil || !receipt.Saved.CommitConfirmed {
				t.Fatal("original typed list save failed/was consumed while parked", receipt.Err)
			}
			active.ProfileID = d.profileID
			waitNeo4jData(t, s, d)
			if d.HasDraft() || d.edit.Err != "" {
				t.Fatal("confirmed typed list save failed", d.edit.Err)
			}
			after, _ := json.Marshal(sibling.page)
			if string(original) != string(after) {
				t.Fatal("list save changed sibling cached observations")
			}
			d.nativeReceipt.Open = true
			captureNeo4jData(t, s, d, "extended-list-"+object.kind+"-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
			d.nativeReceipt.Open = false
			id := d.page.RowRefs[0].TypedValues[0].Text
			pattern := "(e)"
			if object.kind == "relationship" {
				pattern = "()-[e]->()"
			}
			read := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e", map[string]any{"id": id})
			assertGridExtendedLists(t, read.Rows[0][0])
			row := len(d.page.Rows)
			if object.kind == "relationship" {
				publicRelationshipDraft(t, s, d, source, target)
			} else {
				s.addMySQLDataRecord(ui.Services{}, d)
				s.finishMySQLCellEdit(d, false)
			}
			for _, sample := range gridExtendedListSamples() {
				raw, _ := json.Marshal(gridExtendedListValue(t, sample))
				s.EditCell(ui.Services{}, d, row, "property.list_"+sample.kind, contract.Cell{Kind: contract.JSON, Text: string(raw)})
			}
			s.Save(ui.Services{}, d)
			waitNeo4jMutationPreview(t, s, d)
			captureNeo4jData(t, s, d, "extended-list-"+object.kind+"-creation-preview", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
			s.confirmDataMutation(ui.Services{}, d)
			createdReceipt := parkNeo4jSave(t, d)
			if createdReceipt.Err != nil || createdReceipt.Saved == nil || !createdReceipt.Saved.CommitConfirmed || len(createdReceipt.Saved.Outcomes) != 1 || createdReceipt.Saved.Outcomes[0].Saved == nil {
				t.Fatal("list creation missing complete confirmed original outcome", createdReceipt.Err)
			}
			generated := createdReceipt.Saved.Outcomes[0].Saved.Ref.TypedValues[0].Text
			waitNeo4jData(t, s, d)
			row = extendedListRow(t, d, generated)
			if d.HasDraft() || d.edit.Err != "" || d.page.RowRefs[row] == nil {
				t.Fatal("typed list creation did not admit confirmed identity", d.edit.Err)
			}
			read = neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e", map[string]any{"id": generated})
			assertGridExtendedLists(t, read.Rows[0][0])
			// An empty list remains present; its next non-empty edit has an explicit subtype.
			empty, _ := json.Marshal(contract.GraphValue{Kind: "list", Values: []contract.GraphValue{}})
			s.EditCell(ui.Services{}, d, row, "property.list_date", contract.Cell{Kind: contract.JSON, Text: string(empty)})
			s.Save(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if d.edit.Err != "" {
				t.Fatal("empty list clearing failed", d.edit.Err)
			}
			row = extendedListRow(t, d, generated)
			emptyRead := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN $key IN keys(e),e[$key]", map[string]any{"id": generated, "key": "list_date"})
			if emptyRead.Rows[0][0].Text != "true" || emptyRead.Rows[0][1].Kind != "list" || len(emptyRead.Rows[0][1].Values) != 0 {
				t.Fatal("empty list lost explicit property presence")
			}
			filled := contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "date", Text: "2028-02-29"}}}
			raw, _ := json.Marshal(filled)
			s.EditCell(ui.Services{}, d, row, "property.list_date", contract.Cell{Kind: contract.JSON, Text: string(raw)})
			s.Save(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if d.edit.Err != "" {
				t.Fatal("empty list explicit subtype failed", d.edit.Err)
			}
			row = extendedListRow(t, d, generated)
			filledRead := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e.list_date", map[string]any{"id": generated})
			if !reflect.DeepEqual(filledRead.Rows[0][0], filled) {
				t.Fatal("empty list refill targeted another identity")
			}
			// A later row conflict must roll back an earlier complete typed list update.
			point := gridExtendedListSamples()[6]
			point.after = `[{"srid":4326,"x":179,"y":-90},{"srid":4326,"x":180,"y":0}]`
			raw, _ = json.Marshal(gridExtendedListValue(t, point))
			dateRaw, _ := json.Marshal(contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "date", Text: "2030-01-01"}}})
			earlierID, laterID := id, generated
			if earlierID > laterID {
				earlierID, laterID = laterID, earlierID
			}
			beforeRollback := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e", map[string]any{"id": earlierID}).Rows[0][0].Clone()
			s.EditCell(ui.Services{}, d, extendedListRow(t, d, earlierID), "property.list_date", contract.Cell{Kind: contract.JSON, Text: string(dateRaw)})
			s.EditCell(ui.Services{}, d, extendedListRow(t, d, laterID), "property.list_point2d", contract.Cell{Kind: contract.JSON, Text: string(raw)})
			neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id SET e.list_point2d=[point({x:99,y:2})]", map[string]any{"id": laterID})
			s.Save(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if !d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err == "" || !strings.Contains(d.nativeReceipt.JSON, `"RollbackConfirmed":true`) {
				t.Fatal("extended list row conflict lost drafts/rollback", d.edit.Err)
			}
			var rollbackReceipt struct {
				Statements []struct {
					Query  string
					Result contract.GraphResult
				}
			}
			if err := json.Unmarshal([]byte(d.nativeReceipt.JSON), &rollbackReceipt); err != nil {
				t.Fatal(err)
			}
			provisionalUpdates := 0
			for _, statement := range rollbackReceipt.Statements {
				if !strings.Contains(statement.Query, "SET entity += $properties") {
					continue
				}
				provisionalUpdates++
				if len(statement.Result.Rows) != 1 {
					t.Fatal("missing provisional list update observation")
				}
				entity := statement.Result.Rows[0][0]
				var observedID string
				var properties []contract.GraphProperty
				if entity.Node != nil {
					observedID, properties = entity.Node.ElementID, entity.Node.Properties
				} else if entity.Relationship != nil {
					observedID, properties = entity.Relationship.ElementID, entity.Relationship.Properties
				}
				prop := slices.IndexFunc(properties, func(p contract.GraphProperty) bool { return p.Name == "list_date" })
				if observedID != earlierID || prop < 0 || len(properties[prop].Value.Values) != 1 || properties[prop].Value.Values[0].Text != "2030-01-01" {
					t.Fatal("conflict occurred before earlier typed-list mutation")
				}
			}
			if provisionalUpdates != 1 {
				t.Fatal("later conflict did not follow exactly one provisional update", provisionalUpdates)
			}
			captureNeo4jData(t, s, d, "extended-list-"+object.kind+"-conflict")
			read = neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e", map[string]any{"id": earlierID})
			if !reflect.DeepEqual(read.Rows[0][0], beforeRollback) {
				t.Fatal("rollback did not restore complete earlier list entity")
			}
			s.discardMySQLData(d)
			// A stale parked completion retains its original receipt without changing the page.
			s.Load(ui.Services{}, d, true)
			waitNeo4jData(t, s, d)
			s.EditCell(ui.Services{}, d, extendedListRow(t, d, id), "property.list_date", contract.Cell{Kind: contract.JSON, Text: string(dateRaw)})
			s.Save(ui.Services{}, d)
			active.ProfileID = 99
			receipt = parkNeo4jSave(t, d)
			if receipt.Err != nil {
				t.Fatal(receipt.Err)
			}
			fresh, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
			if err != nil {
				t.Fatal(err)
			}
			active.ProfileID, active.Generation = d.profileID, fresh.Generation()
			waitNeo4jData(t, s, d)
			if d.edit.Uncertain == nil || !d.HasDraft() || d.nativeReceipt.JSON != receipt.Saved.NativeJSON {
				t.Fatal("stale list publication lost captured evidence")
			}
			s.Close(d)
			if manager.Generation(d.profileID) != fresh.Generation() || !manager.IsConnected(d.profileID) {
				t.Fatal("old list document closed replacement session")
			}
			active.Generation = fresh.Generation()
		})
	}
}
