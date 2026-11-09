package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicGraphPropertyPresenceOriginalUIConflictsSnapshotsAndGeneration(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaPresence_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Original',exact:$exact})-[r:NYA_PRESENCE]->(b:$($label)) RETURN a,r,b", Parameters: map[string]any{"label": label, "exact": int64(9223372036854775807)}})
	root, relationship := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Relationship.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	if _, err = m.ConnectSession(context.Background(), p, f.Secret); err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	active := true
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, Active: func(*Document) bool { return active }})
	defer s.CloseAll()
	source := "MATCH (a)-[r:NYA_PRESENCE]->(b) WHERE elementId(a)=$id RETURN a,r,b"
	d := s.Open(1, source, p.ID, "neo4j", false)
	d.layoutMode = "result"
	params, _ := json.Marshal(map[string]any{"id": root})
	d.graphParameters = string(params)
	s.Prepare(d)
	waitNativeNeighbors(t, s, d)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	original, _ := json.Marshal(d.result)
	d.graphVisualOpen = true
	g := d.graphVisualState()
	g.focus("n:" + root)
	key := "added ` 猫"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err = tt.Click(locales.Text("新增图属性")); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click(locales.Text("图属性名称")); err != nil {
		t.Fatal(err)
	}
	tt.Type(key)
	captureGraphProperty(t, s, d, "presence-add")
	if err = tt.Click(locales.Text("预览属性保存")); err != nil {
		t.Fatal(err)
	}
	if d.graphPropertyDraft.preview == nil || d.graphPropertyDraft.preview.Before.Kind != "null" || d.graphPropertyDraft.preview.After.Kind != "string" || d.graphPropertyDraft.preview.After.Text != "" {
		t.Fatal("native UI addition confused absence and a present empty string")
	}
	captureGraphProperty(t, s, d, "presence-preview")
	if err = tt.Click(locales.Text("确认新增图属性")); err != nil {
		t.Fatal(err)
	}
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 1 || !d.graphPropertyRecords[0].Receipt.Applied || !s.pinResult(d) {
		t.Fatal("native UI creation failed", d.Error())
	}
	snapshot := d.snapshots[0].Document
	snapshotBytes, _ := json.Marshal(snapshot.graphPropertyRecords)
	d.graphPropertyOpen = false
	captureGraphProperty(t, s, d, "presence-added")
	// The relationship has no properties. Creation must still be available and
	// preserve its directed endpoint identity and the false boolean value.
	g.focus("r:" + relationship)
	s.openGraphPropertyCreation(d, g)
	if d.graphPropertyDraft == nil {
		t.Fatal("empty directed relationship did not allow property creation")
	}
	d.graphPropertyDraft.name, d.graphPropertyDraft.kind, d.graphPropertyDraft.text = key, "boolean", "false"
	s.previewGraphProperty(d)
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Receipt.Applied || len(g.properties()) != 1 || g.properties()[0].Value.Kind != "boolean" || g.properties()[0].Value.Text != "false" {
		t.Fatal("directed relationship creation lost false or native identity", d.Error())
	}
	// Retain an empty-string removal preview, then change the actual property.
	// The old typed comparison must reject removal and retain the draft.
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	tt.Frame()
	if err = tt.Click(key + " " + locales.Text("移除图属性")); err != nil {
		t.Fatal(err)
	}
	s.previewGraphProperty(d)
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n[$key]='Concurrent creation'", Parameters: map[string]any{"id": root, "key": key}})
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft == nil || !d.graphPropertyRecords[2].Receipt.Conflict || d.graphPropertyRecords[2].Receipt.Applied || d.graphPropertyRecords[2].Request.Before.Kind != "string" || d.graphPropertyRecords[2].Request.Before.Text != "" || d.graphPropertyRecords[2].Request.After.Kind != "null" {
		t.Fatal("stale removal erased a concurrent value or lost its typed request")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, "presence-conflict")
	s.verifyGraphProperty(d, 2)
	waitNativeNeighbors(t, s, d)
	if !d.graphPropertyRecords[3].Verification || d.graphPropertyRecords[3].Receipt.Applied {
		t.Fatal("removal verification supplied a false write confirmation")
	}
	d.graphPropertyDraft = nil
	d.graphPropertyOpen = false
	for _, property := range g.properties() {
		if property.Name == key {
			s.openGraphPropertyRemoval(d, g, property)
		}
	}
	s.previewGraphProperty(d)
	if d.graphPropertyDraft == nil || d.graphPropertyDraft.preview == nil || d.graphPropertyDraft.preview.Before.Text != "Concurrent creation" {
		t.Fatal("fresh removal did not use the explicit verification observation")
	}
	captureGraphProperty(t, s, d, "presence-remove")
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || !d.graphPropertyRecords[4].Receipt.Applied {
		t.Fatal("fresh native removal failed", d.Error())
	}
	captureGraphProperty(t, s, d, "presence-removed")
	d.graphPropertyOpen = false
	g.focus("r:" + relationship)
	s.openGraphPropertyRemoval(d, g, g.properties()[0])
	s.previewGraphProperty(d)
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || !d.graphPropertyRecords[5].Receipt.Applied || len(g.properties()) != 0 {
		t.Fatal("relationship removal retained a fake NULL property", d.Error())
	}
	s.verifyGraphProperty(d, 4)
	waitNativeNeighbors(t, s, d)
	if !d.graphPropertyRecords[6].Verification || d.graphPropertyRecords[6].Receipt.Applied {
		t.Fatal("absent-property readback was represented as a removal confirmation")
	}
	captureGraphProperty(t, s, d, "presence-verification")
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting || len(g.node("n:"+root).Value.Properties) != 2 || len(g.edge("r:"+relationship).Value.Properties) != 0 {
		t.Fatal("expansion after removal revived historical properties or conflicts")
	}
	// An acknowledged completion on a parked old generation remains evidence,
	// but cannot update the current graph or resubmit the captured creation.
	s.openGraphPropertyCreation(d, g)
	d.graphPropertyDraft.name, d.graphPropertyDraft.kind, d.graphPropertyDraft.text = key, "integer", "-9223372036854775808"
	s.previewGraphProperty(d)
	s.saveGraphProperty(d)
	active = false
	deadline := time.Now().Add(20 * time.Second)
	for len(d.graphPropertyPending.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.graphPropertyPending.results) == 0 {
		t.Fatal("parked native creation completion was not buffered")
	}
	s.Consume()
	if len(d.graphPropertyRecords) != 7 || !d.Loading() {
		t.Fatal("parked creation was published for another active profile")
	}
	fresh, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	waitNativeNeighbors(t, s, d)
	if !d.NeedsRecovery() || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || len(d.graphPropertyRecords) != 8 || !d.graphPropertyRecords[7].Receipt.Applied || len(g.node("n:"+root).Value.Properties) != 2 {
		t.Fatal("old-generation creation lost evidence or updated the live observation")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, "presence-retired")
	s.saveGraphProperty(d)
	if d.Loading() {
		t.Fatal("retired creation was resubmitted")
	}
	var exported []byte
	complete := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return "presence.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var buffer bytes.Buffer
		err := write(&buffer)
		exported = append([]byte(nil), buffer.Bytes()...)
		close(complete)
		return err
	}}, nil)
	s.exportGraphPropertyRecords(d)
	<-complete
	var records []graphPropertyRecord
	if json.Unmarshal(exported, &records) != nil || len(records) != 8 || records[0].Request.Before.Kind != "null" || records[4].Request.After.Kind != "null" || !records[4].Receipt.Applied || records[7].Request.After.Text != "-9223372036854775808" {
		t.Fatal("presence JSON export lost independent absent/typed request or receipt")
	}
	after, _ := json.Marshal(d.result)
	snapshotAfter, _ := json.Marshal(snapshot.graphPropertyRecords)
	if !bytes.Equal(original, after) || !bytes.Equal(snapshotBytes, snapshotAfter) || len(snapshot.graphVisualState().node("n:"+root).Value.Properties) != 3 || d.Editor.Buffer.String() != source {
		t.Fatal("presence operations modified original query records or the independent snapshot")
	}
	read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (a)-[r:NYA_PRESENCE]->() WHERE elementId(a)=$id RETURN a[$key],r[$key],a.name,a.exact", Mode: "read", Parameters: map[string]any{"id": root, "key": key}})
	if read.Rows[0][0].Kind != "integer" || read.Rows[0][0].Text != "-9223372036854775808" || read.Rows[0][1].Kind != "null" || read.Rows[0][2].Text != "Original" || read.Rows[0][3].Text != "9223372036854775807" {
		t.Fatal("native presence writes changed unrelated values or lost exact types")
	}
	s.Close(d)
	if d.graphPropertyRecords != nil || d.graphPropertyDraft != nil || !snapshot.closed || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("old presence close retained caches or closed the replacement session")
	}
}
