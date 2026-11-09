package dataworkspace

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func publicRelationshipDraft(t *testing.T, s *State, d *Document, source, target string) int {
	t.Helper()
	entityTestClick(t, s, d, "添加记录")
	if d.graphEndpoints == nil {
		t.Fatal("public relationship insertion skipped explicit endpoints")
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	for _, input := range []struct{ label, value string }{{"输入源节点 element ID", source}, {"输入目标节点 element ID", target}} {
		if err := tt.Click(locales.Text(input.label)); err != nil {
			t.Fatal(err)
		}
		tt.Command("selectAll")
		tt.Type(input.value)
	}
	if d.graphEndpoints.source != source || d.graphEndpoints.target != target {
		t.Fatal("explicit native endpoint input did not retain exact IDs")
	}
	entityTestClick(t, s, d, "读取端点")
	waitEndpointRead(t, s, d)
	if d.graphEndpoints == nil || d.graphEndpoints.observation == nil {
		t.Fatal("native endpoint observation missing", d.edit.Err)
	}
	row := len(d.page.Rows)
	entityTestClick(t, s, d, "添加关系草稿")
	s.finishMySQLCellEdit(d, false)
	if len(d.page.Rows) != row+1 || mysqlDataDraftForRow(d, row).GraphEndpoints == nil {
		t.Fatal("native endpoint observation was not staged")
	}
	return row
}

func TestNeo4jNativePublicDataRelationshipCreationTypedDirectedParallelAndSelfLoop(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	label, rel := "NyaPublicRelationship_"+f.Nonce, "NyaPublicRelationshipType_"+f.Nonce+"_猫`"
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	values := map[string]contract.Cell{
		"n": {Kind: contract.Integer, Text: "-9223372036854775808"}, "bytes": {Kind: contract.Binary, Bytes: []byte{0, 255}},
		"empty": {Kind: contract.Text, Text: ""}, "absent": {Kind: contract.Null}, "flag": {Kind: contract.Boolean, Text: "false"}, "float": {Kind: contract.Floating, Text: "-0"},
	}
	expected := map[string]contract.GraphValue{}
	for kind, text := range map[string]string{"integer": "[-9223372036854775808,9223372036854775807]", "string": "[\"\",\"猫\"]", "boolean": "[false,true]", "float": "[-0,1.5]"} {
		v, err := contract.ParseGraphPropertyList(kind, text)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		values["list_"+kind] = contract.Cell{Kind: contract.JSON, Text: string(raw)}
		expected["list_"+kind] = v
	}
	for kind, text := range map[string]string{
		"date": "10000-02-29", "local_time": "23:59:59.999999999", "local_datetime": "-0001-02-28T23:59:59.999999999",
		"time":     `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`,
		"datetime": `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`,
		"duration": `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`,
		"point2d":  `{"srid":4326,"x":181,"y":-90}`, "point3d": `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`,
	} {
		v, err := contract.ParseGraphPropertyExtended(kind, text)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		values[kind] = contract.Cell{Kind: contract.JSON, Text: string(raw)}
		expected[kind] = v
	}
	inventory := map[string]any{"long": "omitted inventory"}
	for key := range values {
		inventory[key] = "inventory"
	}
	seed := neo4jDataRun(t, admin, "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($rel)]->(b) SET r=$inventory RETURN a,b,r", map[string]any{"label": label, "rel": rel, "inventory": inventory})
	source, target := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
	d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("relationship", rel), caps)
	s.Load(ui.Services{}, d, false)
	waitNeo4jData(t, s, d)
	if !d.allowsDataEdit("insert") || d.allowsDataEdit("detach_delete") || s.runtimeFor(d).ReadGraphEndpoints == nil {
		t.Fatal("relationship capabilities not bound")
	}
	entityTestClick(t, s, d, "添加记录")
	captureNeo4jData(t, s, d, "relationship-endpoint-input", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	d.graphEndpoints.source, d.graphEndpoints.target = source, target
	entityTestClick(t, s, d, "读取端点")
	waitEndpointRead(t, s, d)
	if d.graphEndpoints.observation == nil {
		t.Fatal("public original endpoint reader failed")
	}
	captureNeo4jData(t, s, d, "relationship-endpoint-observed", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "添加关系草稿")
	s.finishMySQLCellEdit(d, false)
	row := len(d.page.Rows) - 1
	for key, v := range values {
		s.EditCell(ui.Services{}, d, row, "property."+key, v)
	}
	if len(mysqlDataDraftForRow(d, row).Values) != len(values) {
		t.Fatal("typed relationship properties were dropped", d.edit.Err)
	}
	for _, col := range []string{"start_element_id", "end_element_id", "element_id", "relationship"} {
		if mysqlDataCanEdit(d, row, slices.Index(d.page.Columns, col)) {
			t.Fatal("derived relationship field became writable", col)
		}
	}
	captureNeo4jData(t, s, d, "relationship-typed-draft")
	publicRelationshipDraft(t, s, d, target, source)
	publicRelationshipDraft(t, s, d, source, target)
	publicRelationshipDraft(t, s, d, source, source)
	s.Save(ui.Services{}, d)
	waitNeo4jMutationPreview(t, s, d)
	before, _ := json.Marshal(d.mutation.snapshot)
	copy := d.mutation.plan.Snapshot()
	copy.Edits[0].GraphEndpoints.Source.Labels[0] = "later UI"
	after, _ := json.Marshal(d.mutation.plan.Snapshot())
	if string(before) != string(after) {
		t.Fatal("public original preview borrowed UI endpoint values")
	}
	captureNeo4jData(t, s, d, "relationship-typed-preview", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	observed := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", map[string]any{"rel": rel})
	if observed.Rows[0][0].Text != "1" {
		t.Fatal("relationship preview mutated graph")
	}
	entityTestClick(t, s, d, "确认并提交更改")
	receipt := parkNeo4jSave(t, d)
	if receipt.Err != nil || receipt.Saved == nil || !receipt.Saved.CommitConfirmed || receipt.Saved.Applied != 4 || len(receipt.Saved.Outcomes) != 4 {
		t.Fatal("public relationship batch not committed", receipt.Err)
	}
	var native struct {
		Statements []struct {
			Query  string
			Result contract.GraphResult
		}
		Bookmarks       []string
		CommitConfirmed bool
	}
	if json.Unmarshal([]byte(receipt.Saved.NativeJSON), &native) != nil || !native.CommitConfirmed || len(native.Bookmarks) == 0 {
		t.Fatal("native relationship commit evidence missing")
	}
	locks, created := 0, int64(0)
	for _, statement := range native.Statements {
		if strings.Contains(statement.Query, "SET entity[$lock_key]") {
			locks++
		}
		created += statement.Result.Summary.Counters["relationships_created"]
	}
	if locks != 2 || created != 4 {
		t.Fatal("public creation omitted unique endpoint locks/counters", locks, created)
	}
	generated := receipt.Saved.Outcomes[0].Saved
	if generated == nil {
		t.Fatal("actual COMMIT lost generated relationship identity")
	}
	id := generated.Ref.TypedValues[0].Text
	original := s.runtimeFor(d)
	updated, err := original.Saver(context.Background(), d.profileID, d.database, d.table, []contract.DataEdit{{Kind: "update", Original: cloneSnapshot(generated), Values: map[string]contract.DataValue{"property.long": {Kind: contract.Text, Text: "continued signed relationship"}}}})
	if err != nil || updated == nil || !updated.CommitConfirmed {
		t.Fatal("generated relationship cannot edit absent inventory field", err)
	}
	waitNeo4jData(t, s, d)
	if d.HasDraft() || d.edit.Uncertain != nil || len(d.page.Rows) != 5 {
		t.Fatal("public confirmed relationship batch not accepted", d.edit.Err)
	}
	row = slices.IndexFunc(d.page.RowRefs, func(r *contract.RowRef) bool { return r != nil && r.TypedValues[0].Text == id })
	s.EditCell(ui.Services{}, d, row, "property.long", contract.Cell{Kind: contract.Text, Text: "continued UI relationship"})
	s.Save(ui.Services{}, d)
	waitNeo4jData(t, s, d)
	if d.HasDraft() || d.edit.Err != "" {
		t.Fatal("continued shared-grid relationship property edit failed", d.edit.Err)
	}
	read := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE elementId(r)=$id RETURN r", map[string]any{"id": id})
	r := read.Rows[0][0].Relationship
	if r.StartElementID != source || r.EndElementID != target || r.Type != rel {
		t.Fatal("public relationship direction/type changed")
	}
	props := map[string]contract.GraphValue{}
	for _, p := range r.Properties {
		props[p.Name] = p.Value
	}
	for key, v := range expected {
		if !reflect.DeepEqual(props[key], v) {
			t.Fatal("public creation changed exact native kind", key, props[key], v)
		}
	}
	if props["n"].Text != "-9223372036854775808" || props["bytes"].Text != "AP8=" || props["empty"].Kind != "string" || props["empty"].Text != "" || props["long"].Text != "continued UI relationship" {
		t.Fatal("scalar/bytes/empty/continued values changed")
	}
	if _, exists := props["absent"]; exists {
		t.Fatal("absent property stored as NULL")
	}
	d.nativeReceipt.Open = true
	captureNeo4jData(t, s, d, "relationship-continued-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
	d.nativeReceipt.Open = false
	empty := s.OpenObject(2, manager.Namespace(active.ProfileID, "neo4j").Object("relationship", rel+"_empty"), caps)
	s.Load(ui.Services{}, empty, false)
	waitNeo4jData(t, s, empty)
	publicRelationshipDraft(t, s, empty, source, target)
	if len(mysqlDataDraftForRow(empty, 0).Values) != 0 || empty.edit.Editor != nil {
		t.Fatal("propertyless relationship invented attributes")
	}
	captureNeo4jData(t, s, empty, "relationship-propertyless-draft")
	s.Save(ui.Services{}, empty)
	waitNeo4jMutationPreview(t, s, empty)
	s.confirmDataMutation(ui.Services{}, empty)
	waitNeo4jData(t, s, empty)
	if empty.HasDraft() || empty.edit.Err != "" || empty.page.RowRefs[0] == nil {
		t.Fatal("propertyless relationship failed", empty.edit.Err)
	}
	read = neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN r", map[string]any{"rel": rel + "_empty"})
	if len(read.Rows) != 1 || len(read.Rows[0][0].Relationship.Properties) != 0 {
		t.Fatal("propertyless native creation changed semantics")
	}
}

func TestNeo4jNativePublicDataRelationshipEndpointChangesAndAtomicRollback(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	for _, change := range []string{"source_property", "source_label", "target_property", "target_delete", "later_relationship"} {
		t.Run(change, func(t *testing.T) {
			label, rel := "NyaPublicEndpointConflict_"+f.Nonce+"_"+change, "NyaPublicEndpointType_"+f.Nonce+"_"+change
			defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
			seed := neo4jDataRun(t, admin, "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($rel) {guard:1}]->(b) RETURN a,b,r", map[string]any{"label": label, "rel": rel})
			source, target, old := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID, seed.Rows[0][2].Relationship.ElementID
			d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("relationship", rel), caps)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			row := publicRelationshipDraft(t, s, d, source, target)
			if change == "later_relationship" {
				s.EditCell(ui.Services{}, d, row, "property.guard", contract.Cell{Kind: contract.Integer, Text: "111"})
				s.EditCell(ui.Services{}, d, 0, "property.guard", contract.Cell{Kind: contract.Integer, Text: "222"})
				// Put the insert first without adopting different row identities, so a real
				// subsequent existing-row conflict must roll back a provisional creation.
				d.page.Rows[0], d.page.Rows[row] = d.page.Rows[row], d.page.Rows[0]
				d.page.RowRefs[0], d.page.RowRefs[row] = d.page.RowRefs[row], d.page.RowRefs[0]
				d.page.RowSnapshots[0], d.page.RowSnapshots[row] = d.page.RowSnapshots[row], d.page.RowSnapshots[0]
				for _, draft := range d.edit.Drafts {
					draft.Row = row - draft.Row
				}
			}
			s.Save(ui.Services{}, d)
			waitNeo4jMutationPreview(t, s, d)
			query := "MATCH (n) WHERE elementId(n)=$source SET n.guard=999"
			if change == "source_label" {
				query = "MATCH (n) WHERE elementId(n)=$source SET n:NYA_CHANGED_PUBLIC_ENDPOINT"
			}
			if change == "target_property" {
				query = "MATCH (n) WHERE elementId(n)=$target SET n.guard=999"
			}
			if change == "target_delete" {
				query = "MATCH (n) WHERE elementId(n)=$target DETACH DELETE n"
			}
			if change == "later_relationship" {
				query = "MATCH ()-[r]->() WHERE elementId(r)=$old SET r.guard=-999"
			}
			neo4jDataRun(t, admin, query, map[string]any{"source": source, "target": target, "old": old})
			s.confirmDataMutation(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if !d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err == "" || !strings.Contains(d.nativeReceipt.JSON, `"RollbackConfirmed":true`) {
				t.Fatal("public endpoint conflict lost drafts or failed rollback", d.edit.Err)
			}
			if change == "source_property" {
				captureNeo4jData(t, s, d, "relationship-endpoint-conflict")
			}
			read := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN r.guard", map[string]any{"rel": rel})
			wanted := 1
			if change == "target_delete" {
				wanted = 0
			}
			if len(read.Rows) != wanted {
				t.Fatal("public endpoint conflict left provisional relationship")
			}
			if change == "later_relationship" && (read.Rows[0][0].Text != "-999" || !strings.Contains(d.nativeReceipt.JSON, `"relationships_created":1`)) {
				t.Fatal("later relationship conflict did not prove provisional creation and atomic rollback")
			}
			s.Close(d)
		})
	}
}

// A genuine operation notification can be parked without admitting its data.
func awaitPublicEndpointResult(t *testing.T, d *Document) dataGraphEndpointResult {
	t.Helper()
	select {
	case r := <-d.graphEndpoints.results:
		out := make(chan dataGraphEndpointResult, 1)
		out <- r
		d.graphEndpoints.results = out
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("native original endpoint result stalled")
	}
	return dataGraphEndpointResult{}
}
