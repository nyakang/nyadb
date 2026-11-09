package dataworkspace

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/locales"
)

func waitNeo4jMutationPreview(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for d.mutation.results != nil && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.mutation.results != nil || !d.mutation.open || d.mutation.plan == nil {
		t.Fatal("original public entity preview missing", d.edit.Err)
	}
}
func openNeo4jEntityWorkbench(t *testing.T, f neo4jDataFixture) (*neo4j.Client, *dbruntime.Manager, *State, *Session, contract.Capabilities) {
	t.Helper()
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	t.Cleanup(func() { manager.Close() })
	profile := neo4jDataProfile(t, f)
	handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active := &Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return *active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: manager.Generation(id), Connected: manager.IsConnected(id)}
	}, RuntimeFor: manager.Data})
	t.Cleanup(s.CloseAll)
	return admin, manager, s, active, handle.Capabilities()
}
func entityTestClick(t *testing.T, s *State, d *Document, label string) {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err, d.edit.Err)
	}
}

func TestNeo4jNativePublicDataEntityCreationTypedOmittedAndPropertylessConfirmedIdentity(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	label := "NyaPublicEntityCreate_" + f.Nonce
	neo4jDataRun(t, admin, "CREATE (n:$($label) {n:1,text:'inventory',empty:'x',bytes:$bytes,long:$long})", map[string]any{"label": label, "bytes": []byte{1}, "long": strings.Repeat("inventory 猫 ", 1000)})
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	object := manager.Namespace(active.ProfileID, "neo4j").Object("label", label)
	d := s.OpenObject(1, object, caps)
	s.Load(ui.Services{}, d, false)
	waitNeo4jData(t, s, d)
	if !d.edit.Batch || !s.runtimeFor(d).InsertOmittedValues || !d.allowsDataEdit("insert") || !d.allowsDataEdit("detach_delete") {
		t.Fatal("original public node capabilities not bound", d.err)
	}
	originalCount := len(d.page.Rows)
	entityTestClick(t, s, d, "添加记录")
	s.finishMySQLCellEdit(d, false)
	row := len(d.page.Rows) - 1
	for _, value := range mysqlDataDraftForRow(d, row).Values {
		if value.Default {
			t.Fatal("node creation emitted SQL DEFAULT")
		}
	}
	s.EditCell(ui.Services{}, d, row, "property.n", contract.Cell{Kind: contract.Integer, Text: "-9223372036854775808"})
	s.EditCell(ui.Services{}, d, row, "property.bytes", contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255}})
	s.EditCell(ui.Services{}, d, row, "property.empty", contract.Cell{Kind: contract.Text, Text: ""})
	s.EditCell(ui.Services{}, d, row, "property.text", contract.Cell{Kind: contract.Null})
	captureNeo4jData(t, s, d, "entity-create-typed-draft")
	entityTestClick(t, s, d, "保存数据更改")
	waitNeo4jMutationPreview(t, s, d)
	if d.page.RowRefs[row] != nil || len(d.mutation.snapshot.Items) != 1 || d.mutation.snapshot.Items[0].Kind != "insert" {
		t.Fatal("preview generated provisional identity")
	}
	var preview struct{ Edits []contract.DataEdit }
	if json.Unmarshal([]byte(d.mutation.snapshot.NativeJSON), &preview) != nil || len(preview.Edits) != 1 || preview.Edits[0].Values["property.n"].Kind != contract.Integer || preview.Edits[0].Values["property.n"].Text != "-9223372036854775808" {
		t.Fatal("original exact typed preview missing")
	}
	observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
	if observed.Rows[0][0].Text != "1" {
		t.Fatal("preview wrote a node before confirmation")
	}
	captureNeo4jData(t, s, d, "entity-create-typed-preview", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "确认并提交更改")
	receipt := parkNeo4jSave(t, d)
	if receipt.Err != nil || receipt.Saved == nil || !receipt.Saved.CommitConfirmed || len(receipt.Saved.Outcomes) != 1 || receipt.Saved.Outcomes[0].Saved == nil {
		t.Fatal("original generated identity lacks actual commit acknowledgement", receipt.Err)
	}
	generated := receipt.Saved.Outcomes[0].Saved
	id := generated.Ref.TypedValues[0].Text
	omitted := neo4jDataRun(t, admin, "MATCH (n) WHERE elementId(n)=$id RETURN n.long", map[string]any{"id": id})
	if len(omitted.Rows) != 1 || omitted.Rows[0][0].Kind != "null" {
		t.Fatal("omitted inventory property was written during creation")
	}
	// Use the original signed outcome before refresh, including an absent inventory
	// property, through the owning public runtime rather than a replacement lease.
	signed, err := s.runtimeFor(d).Saver(context.Background(), d.profileID, d.database, d.table, []contract.DataEdit{{Kind: "update", Original: cloneSnapshot(generated), Values: map[string]contract.DataValue{"property.long": {Kind: contract.Text, Text: "signed outcome before refresh"}}}})
	if err != nil || signed == nil || !signed.CommitConfirmed {
		t.Fatal("signed generated outcome cannot edit an absent inventory field", err)
	}
	waitNeo4jData(t, s, d)
	row = slices.IndexFunc(d.page.RowRefs, func(ref *contract.RowRef) bool {
		return ref != nil && len(ref.TypedValues) == 1 && ref.TypedValues[0].Text == id
	})
	if row < 0 {
		t.Fatal("confirmed generated identity missing from refreshed original page")
	}
	if d.edit.Err != "" || d.HasDraft() || len(d.page.Rows) != originalCount+1 || d.page.RowRefs[row] == nil || d.page.RowSnapshots[row] == nil || d.edit.Uncertain != nil {
		t.Fatal("actual confirmed node creation not accepted", d.edit.Err)
	}
	result := neo4jDataRun(t, admin, "MATCH (n) WHERE elementId(n)=$id RETURN n.n,n.bytes,n.empty,n.text,n.long,labels(n)", map[string]any{"id": id})
	if len(result.Rows) != 1 || result.Rows[0][0].Text != "-9223372036854775808" || result.Rows[0][1].Kind != "bytes" || result.Rows[0][1].Text != "AP8=" || result.Rows[0][2].Kind != "string" || result.Rows[0][2].Text != "" || result.Rows[0][3].Kind != "null" || (result.Rows[0][4].Kind != "string" || result.Rows[0][4].Text != "signed outcome before refresh") {
		t.Fatalf("typed creation collapsed empty/null/omitted values: %+v", result.Rows)
	}
	// The original signed insert outcome includes absent inventory fields, so
	// continued property edits use the original owning document and new signature.
	s.EditCell(ui.Services{}, d, row, "property.long", contract.Cell{Kind: contract.Text, Text: "continued absent property 猫"})
	s.Save(ui.Services{}, d)
	waitNeo4jData(t, s, d)
	if d.edit.Err != "" || d.HasDraft() {
		t.Fatal("continued absent inventory property edit failed", d.edit.Err)
	}
	d.nativeReceipt.Open = true
	captureNeo4jData(t, s, d, "entity-create-continued-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
	d.nativeReceipt.Open = false
	// A label with no existing properties needs no invented writable field.
	empty := label + "_empty"
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": empty})
	emptyDoc := s.OpenObject(2, manager.Namespace(active.ProfileID, "neo4j").Object("label", empty), caps)
	s.Load(ui.Services{}, emptyDoc, false)
	waitNeo4jData(t, s, emptyDoc)
	entityTestClick(t, s, emptyDoc, "添加记录")
	if emptyDoc.edit.Editor != nil || len(mysqlDataDraftForRow(emptyDoc, 0).Values) != 0 {
		t.Fatal("propertyless node invented properties")
	}
	captureNeo4jData(t, s, emptyDoc, "entity-create-propertyless-draft")
	s.Save(ui.Services{}, emptyDoc)
	waitNeo4jMutationPreview(t, s, emptyDoc)
	s.confirmDataMutation(ui.Services{}, emptyDoc)
	waitNeo4jData(t, s, emptyDoc)
	if emptyDoc.edit.Err != "" || emptyDoc.HasDraft() || emptyDoc.page.RowRefs[0] == nil {
		t.Fatal("propertyless node creation failed", emptyDoc.edit.Err)
	}
	result = neo4jDataRun(t, admin, "MATCH (n) WHERE elementId(n)=$id RETURN properties(n),labels(n)", map[string]any{"id": emptyDoc.page.RowRefs[0].TypedValues[0].Text})
	if len(result.Rows) != 1 || len(result.Rows[0][0].Properties) != 0 || len(result.Rows[0][1].Values) != 1 || result.Rows[0][1].Values[0].Text != empty {
		t.Fatal("propertyless creation invented values or wrong label")
	}
}

func TestNeo4jNativePublicDataEntityDeleteChoiceBlockedAndAdjacentBatchConfirmed(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	label, rel, outside := "NyaPublicEntityDelete_"+f.Nonce, "NyaPublicEntityRel_"+f.Nonce, "NyaPublicOutside_"+f.Nonce
	neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1}),(b:$($label) {n:2}),(c:$($outside) {n:3}) CREATE (a)-[:$($rel) {n:1}]->(b),(a)-[:$($rel) {n:2}]->(b),(b)-[:$($rel) {n:3}]->(a),(a)-[:$($rel) {n:4}]->(a),(b)-[:$($rel) {n:5}]->(b),(b)-[:$($rel) {n:6}]->(c)", map[string]any{"label": label, "rel": rel, "outside": outside})
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) OR $outside IN labels(n) DETACH DELETE n", map[string]any{"label": label, "outside": outside})
	d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("label", label), caps)
	s.Load(ui.Services{}, d, false)
	waitNeo4jData(t, s, d)
	d.selectedRow = 0
	d.selection.Add(0)
	d.selection.Add(1)
	entityTestClick(t, s, d, "删除记录")
	if d.deleteChoice == nil || d.HasDraft() {
		t.Fatal("deletion command skipped original selection decision")
	}
	captureNeo4jData(t, s, d, "entity-delete-choice", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "只删除节点")
	if len(d.edit.Drafts) != 2 {
		t.Fatal("plain choice lost selected adjacent nodes")
	}
	s.Save(ui.Services{}, d)
	waitNeo4jMutationPreview(t, s, d)
	if !dataMutationBlocked(d.mutation.snapshot) || d.mutation.snapshot.Items[0].AffectedRelationships < 4 || d.mutation.snapshot.Items[1].AffectedRelationships < 4 {
		t.Fatal("plain connected-node deletion was not blocked")
	}
	captureNeo4jData(t, s, d, "entity-delete-plain-blocked", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	s.confirmDataMutation(ui.Services{}, d)
	if d.edit.Results != nil || d.nativeReceipt.JSON != "" {
		t.Fatal("blocked batch sent a write")
	}
	observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
	if observed.Rows[0][0].Text != "2" {
		t.Fatal("plain preview changed live nodes")
	}
	entityTestClick(t, s, d, "取消")
	if !d.HasDraft() || d.edit.Busy {
		t.Fatal("preview cancellation lost original deletion drafts")
	}
	s.discardMySQLData(d)
	d.selectedRow = 0
	d.selection.Add(0)
	d.selection.Add(1)
	entityTestClick(t, s, d, "删除记录")
	entityTestClick(t, s, d, "删除节点和关联关系")
	s.Save(ui.Services{}, d)
	waitNeo4jMutationPreview(t, s, d)
	if dataMutationBlocked(d.mutation.snapshot) || len(d.mutation.snapshot.Items) != 2 || !slices.Contains(d.boundRuntime.PreviewEditKinds, "detach_delete") {
		t.Fatal("explicit detached batch preview not bound")
	}
	captureNeo4jData(t, s, d, "entity-delete-detach-preview", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "确认并提交更改")
	waitNeo4jData(t, s, d)
	if d.edit.Err != "" || d.HasDraft() || len(d.page.Rows) != 0 || !strings.Contains(d.nativeReceipt.JSON, `"CommitConfirmed":true`) {
		t.Fatal("adjacent node batch deletion not acknowledged", d.edit.Err)
	}
	observed = neo4jDataRun(t, admin, "MATCH (n) WHERE $outside IN labels(n) RETURN n.n", map[string]any{"outside": outside})
	if len(observed.Rows) != 1 || observed.Rows[0][0].Text != "3" {
		t.Fatal("unselected endpoint was removed")
	}
	observed = neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", map[string]any{"rel": rel})
	if observed.Rows[0][0].Text != "0" {
		t.Fatal("distinct shared/reverse/parallel/self relationships not removed")
	}
	d.nativeReceipt.Open = true
	captureNeo4jData(t, s, d, "entity-delete-detach-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
	d.nativeReceipt.Open = false
	// Relationship deletion cannot invent an endpoint choice or allow insertion.
	neo4jDataRun(t, admin, "CREATE (a:$($label)),(b:$($label)) CREATE (a)-[:$($rel) {n:7}]->(b)", map[string]any{"label": label, "rel": rel})
	r := s.OpenObject(2, manager.Namespace(active.ProfileID, "neo4j").Object("relationship", rel), caps)
	s.Load(ui.Services{}, r, false)
	waitNeo4jData(t, s, r)
	if r.allowsDataEdit("insert") || r.allowsDataEdit("detach_delete") {
		t.Fatal("relationship endpoints invented")
	}
	r.selectedRow = 0
	r.selection.Add(0)
	entityTestClick(t, s, r, "删除记录")
	if r.deleteChoice != nil {
		t.Fatal("relationship delete used node decision")
	}
	s.Save(ui.Services{}, r)
	waitNeo4jMutationPreview(t, s, r)
	s.confirmDataMutation(ui.Services{}, r)
	waitNeo4jData(t, s, r)
	if r.edit.Err != "" || r.HasDraft() || len(r.page.Rows) != 0 {
		t.Fatal("relationship deletion failed", r.edit.Err)
	}
	observed = neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
	if observed.Rows[0][0].Text != "2" {
		t.Fatal("relationship deletion removed its endpoints")
	}
}

func TestNeo4jNativePublicDataEntityPreviewChangedObservationRollsBackOriginalBatch(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	for i, change := range []string{"node-property", "relationship-property", "added", "removed", "direction", "type", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			label := "NyaPublicEntityConflict_" + f.Nonce + "_" + strings.ReplaceAll(change, "-", "_")
			outside, rel := label+"_outside", label+"_rel"
			seed := neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1}),(b:$($label) {n:2}),(c:$($outside) {n:3}),(other:$($outside) {n:4}) RETURN a,b,c,other", map[string]any{"label": label, "outside": outside, "rel": rel})
			defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) OR $outside IN labels(n) DETACH DELETE n", map[string]any{"label": label, "outside": outside})
			d := s.OpenObject(uint64(10+i), manager.Namespace(active.ProfileID, "neo4j").Object("label", label), caps)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)

			firstID, secondID := d.page.RowRefs[0].TypedValues[0].Text, d.page.RowRefs[1].TypedValues[0].Text
			edges := neo4jDataRun(t, admin, "MATCH (first),(second),(c) WHERE elementId(first)=$first AND elementId(second)=$second AND elementId(c)=$destination CREATE (first)-[:$($rel) {n:1}]->(first),(second)-[r:$($rel) {n:1}]->(c) RETURN r", map[string]any{"first": firstID, "second": secondID, "destination": seed.Rows[0][2].Node.ElementID, "rel": rel})
			d.selectedRow = 0
			d.selection.Add(0)
			d.selection.Add(1)
			s.deleteMySQLDataRecords(ui.Services{}, d, 0)
			s.chooseDataDeleteKind(ui.Services{}, d, "detach_delete")
			cached := d.page
			s.Save(ui.Services{}, d)
			waitNeo4jMutationPreview(t, s, d)
			edge, source, destination, other := edges.Rows[0][0].Relationship.ElementID, secondID, seed.Rows[0][2].Node.ElementID, seed.Rows[0][3].Node.ElementID
			params := map[string]any{"edge": edge, "source": source, "destination": destination, "other": other, "rel": rel}
			switch change {
			case "node-property":
				neo4jDataRun(t, admin, "MATCH (b) WHERE elementId(b)=$source SET b.n=999", params)
			case "relationship-property":
				neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE elementId(r)=$edge SET r.n=999", params)
			case "added":
				neo4jDataRun(t, admin, "MATCH (b),(c) WHERE elementId(b)=$source AND elementId(c)=$destination CREATE (b)-[:$($rel) {n:999}]->(c)", params)
			case "removed":
				neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE elementId(r)=$edge DELETE r", params)
			case "direction":
				neo4jDataRun(t, admin, "MATCH (b)-[r]->(c) WHERE elementId(r)=$edge DELETE r CREATE (c)-[:$($rel) {n:1}]->(b)", params)
			case "type":
				neo4jDataRun(t, admin, "MATCH (b)-[r]->(c) WHERE elementId(r)=$edge DELETE r CREATE (b)-[:NYA_PUBLIC_CHANGED_TYPE {n:1}]->(c)", params)
			case "endpoint":
				neo4jDataRun(t, admin, "MATCH (b)-[r]->(c),(other) WHERE elementId(r)=$edge AND elementId(other)=$other DELETE r CREATE (b)-[:$($rel) {n:1}]->(other)", params)
			}
			s.confirmDataMutation(ui.Services{}, d)
			waitNeo4jData(t, s, d)
			if d.edit.Err == "" || d.edit.Uncertain != nil || len(d.edit.Drafts) != 2 || d.page != cached || !strings.Contains(d.nativeReceipt.JSON, `"RollbackConfirmed":true`) {
				t.Fatal("changed original entity preview lost drafts or failed to roll back", change, d.edit.Err)
			}
			var receipt struct {
				Statements []struct{ Result neo4j.Response }
			}
			if err := json.Unmarshal([]byte(d.nativeReceipt.JSON), &receipt); err != nil {
				t.Fatal("original conflict receipt missing", err)
			}
			var deletedNodes, deletedRelationships int64
			for _, statement := range receipt.Statements {
				deletedNodes += statement.Result.Summary.Counters["nodes_deleted"]
				deletedRelationships += statement.Result.Summary.Counters["relationships_deleted"]
			}
			if deletedNodes != 1 || deletedRelationships != 1 {
				t.Fatal("conflict must follow the first node and self relationship's provisional deletion", deletedNodes, deletedRelationships)
			}
			observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
			if observed.Rows[0][0].Text != "2" {
				t.Fatal("later conflict did not restore first provisional node deletion")
			}
			observed = neo4jDataRun(t, admin, "MATCH (a)-[r]->(a) WHERE elementId(a)=$id RETURN r.n", map[string]any{"id": firstID})
			if len(observed.Rows) != 1 || observed.Rows[0][0].Text != "1" {
				t.Fatal("later conflict did not restore first provisional self relationship deletion")
			}
			if change == "relationship-property" {
				captureNeo4jData(t, s, d, "entity-preview-conflict")
			}
			s.Close(d)
		})
	}
}
