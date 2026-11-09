package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicGraphPropertyListsOriginalUIReceiptsSnapshotsAndGeneration(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaLists_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Original 猫',values:[0],exact:$exact})-[r:NYA_LISTS {weights:[1.0]}]->(b:$($label)) RETURN a,r,b", Parameters: map[string]any{"label": label, "exact": int64(9223372036854775807)}})
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
	source := "MATCH (a)-[r:NYA_LISTS]->(b) WHERE elementId(a)=$id RETURN a,r,b"
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
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	click := func(label string) {
		t.Helper()
		tt.Frame()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(key, property, text string) {
		t.Helper()
		d.graphPropertyOpen = false
		g.focus(key)
		click(property + " " + locales.Text("编辑图属性"))
		if d.graphPropertyDraft == nil {
			t.Fatal("native list draft unavailable", property)
		}
		d.graphPropertyDraft.text = text
	}
	apply := func(confirm string) int {
		t.Helper()
		click(locales.Text("预览属性保存"))
		if d.graphPropertyDraft.preview == nil {
			t.Fatal("native list preview unavailable", d.graphPropertyDraft.error)
		}
		index := len(d.graphPropertyRecords)
		click(locales.Text(confirm))
		waitNativeNeighbors(t, s, d)
		if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != index+1 || !d.graphPropertyRecords[index].Receipt.Applied || !d.graphPropertyRecords[index].Receipt.Result.Completed {
			t.Fatal("native list save lost complete receipt", d.Error())
		}
		return index
	}
	property := func(name string) contract.GraphValue {
		t.Helper()
		for _, property := range g.properties() {
			if property.Name == name {
				return property.Value.Clone()
			}
		}
		return contract.GraphValue{Kind: "null"}
	}
	click("values " + locales.Text("编辑图属性"))
	if d.graphPropertyDraft == nil || d.graphPropertyDraft.kind != "list:integer" || d.graphPropertyDraft.text != "[0]" {
		t.Fatal("existing native integer list lost its editing type")
	}
	captureGraphProperty(t, s, d, "list-edit")
	click(locales.Text("属性新值"))
	tt.Key(commandModifier(), ui.KeyA)
	tt.Type("[-9223372036854775808,9007199254740993,9223372036854775807]")
	click(locales.Text("预览属性保存"))
	exact := graphListValueTest(t, "integer", "[-9223372036854775808,9007199254740993,9223372036854775807]")
	if d.graphPropertyDraft.preview == nil || !reflect.DeepEqual(d.graphPropertyDraft.preview.After, exact) {
		t.Fatal("native list UI input rounded exact integer tokens", d.graphPropertyDraft.error)
	}
	captureGraphProperty(t, s, d, "list-preview")
	click(locales.Text("确认保存图属性"))
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 1 || !d.graphPropertyRecords[0].Receipt.Applied || !reflect.DeepEqual(property("values"), exact) {
		t.Fatal("native list receipt did not update the explicit observation", d.Error())
	}
	captureGraphProperty(t, s, d, "list-receipt")
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snapshot := d.snapshots[0].Document
	snapshotBytes, _ := json.Marshal(snapshot.graphPropertyRecords)
	// A directed relationship retains the explicitly observed float element
	// type even when JSON contains integral-looking numeric tokens.
	edit("r:"+relationship, "weights", "[-0.0,1,2.5]")
	apply("确认保存图属性")
	weights := graphListValueTest(t, "float", "[-0,1,2.5]")
	if !reflect.DeepEqual(property("weights"), weights) {
		t.Fatal("directed list save lost float type, signed zero or endpoints")
	}
	d.graphPropertyOpen = false
	click(locales.Text("新增图属性"))
	key := "added ` 猫 list"
	click(locales.Text("图属性名称"))
	tt.Type(key)
	click(locales.Text("图属性类型"))
	click(locales.Text("字符串列表"))
	if d.graphPropertyDraft.kind != "list:string" || d.graphPropertyDraft.text != "[]" || d.graphPropertyDraft.preview != nil {
		t.Fatal("native type chooser retained a scalar or stale preview")
	}
	captureGraphProperty(t, s, d, "list-add")
	apply("确认新增图属性")
	if got := property(key); got.Kind != "list" || len(got.Values) != 0 {
		t.Fatal("native empty list creation became an absent property")
	}
	edit("r:"+relationship, key, "[]")
	click(locales.Text("图属性类型"))
	click(locales.Text("布尔值列表"))
	d.graphPropertyDraft.text = "[false,true]"
	apply("确认保存图属性")
	if !reflect.DeepEqual(property(key), graphListValueTest(t, "boolean", "[false,true]")) {
		t.Fatal("explicit empty-list type selection lost false")
	}
	edit("r:"+relationship, key, "[]")
	apply("确认保存图属性")
	captureGraphProperty(t, s, d, "list-empty")
	if got := property(key); got.Kind != "list" || len(got.Values) != 0 {
		t.Fatal("clearing a native list removed its property")
	}
	d.graphPropertyOpen = false
	click(key + " " + locales.Text("移除图属性"))
	captureGraphProperty(t, s, d, "list-remove")
	apply("确认移除图属性")
	if property(key).Kind != "null" || d.graphPropertyRecords[5].Request.Before.Kind != "list" || d.graphPropertyRecords[5].Request.After.Kind != "null" {
		t.Fatal("native list removal retained fake NULL or confused empty with absent")
	}
	// Concurrent changes reject a captured typed old list; explicit readback
	// remains separate evidence and supplies the next deliberate editing basis.
	edit("n:"+root, "values", "[42]")
	click(locales.Text("预览属性保存"))
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n.values=$value", Parameters: map[string]any{"id": root, "value": []int64{9007199254740993}}})
	click(locales.Text("确认保存图属性"))
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft == nil || len(d.graphPropertyRecords) != 7 || !d.graphPropertyRecords[6].Receipt.Conflict || d.graphPropertyRecords[6].Receipt.Applied || !reflect.DeepEqual(d.graphPropertyRecords[6].Request.Before, exact) {
		t.Fatal("stale native list comparison erased a concurrent value or lost draft")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, "list-conflict")
	s.verifyGraphProperty(d, 6)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 8 || !d.graphPropertyRecords[7].Verification || d.graphPropertyRecords[7].Receipt.Applied || !reflect.DeepEqual(property("values"), graphListValueTest(t, "integer", "[9007199254740993]")) {
		t.Fatal("native list readback supplied a false write confirmation")
	}
	d.graphPropertyDraft = nil
	edit("n:"+root, "values", "[1,2]")
	apply("确认保存图属性")
	d.graphPropertyOpen = false
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting || !reflect.DeepEqual(property("values"), graphListValueTest(t, "integer", "[1,2]")) {
		t.Fatal("neighbor expansion revived superseded lists after continued editing")
	}
	// An acknowledged write parked on the original generation remains an
	// immutable receipt; it must not update replacement observations or replay.
	edit("n:"+root, "values", "[-9223372036854775808,9223372036854775807]")
	click(locales.Text("预览属性保存"))
	click(locales.Text("确认保存图属性"))
	active = false
	deadline := time.Now().Add(20 * time.Second)
	for len(d.graphPropertyPending.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.graphPropertyPending.results) == 0 {
		t.Fatal("parked native list completion was not buffered")
	}
	s.Consume()
	if len(d.graphPropertyRecords) != 9 || !d.Loading() {
		t.Fatal("parked native list receipt published for another profile")
	}
	fresh, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	waitNativeNeighbors(t, s, d)
	if !d.NeedsRecovery() || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || len(d.graphPropertyRecords) != 10 || !d.graphPropertyRecords[9].Receipt.Applied || !reflect.DeepEqual(property("values"), graphListValueTest(t, "integer", "[1,2]")) {
		t.Fatal("retired native list write discarded evidence or replaced current observation")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, "list-retired")
	s.saveGraphProperty(d)
	if d.Loading() {
		t.Fatal("retired native list write was replayed")
	}
	var exported []byte
	complete := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return "lists.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var buffer bytes.Buffer
		err := write(&buffer)
		exported = append([]byte(nil), buffer.Bytes()...)
		close(complete)
		return err
	}}, nil)
	s.exportGraphPropertyRecords(d)
	<-complete
	var records []graphPropertyRecord
	if json.Unmarshal(exported, &records) != nil || len(records) != 10 || !reflect.DeepEqual(records[0].Request.After, exact) || records[2].Request.Before.Kind != "null" || records[2].Request.After.Kind != "list" || records[5].Request.After.Kind != "null" || !records[9].Receipt.Applied {
		t.Fatal("native list JSON export lost typed exact elements, empty/absence or independent receipt")
	}
	after, _ := json.Marshal(d.result)
	snapshotAfter, _ := json.Marshal(snapshot.graphPropertyRecords)
	if !bytes.Equal(original, after) || !bytes.Equal(snapshotBytes, snapshotAfter) || d.Editor.Buffer.String() != source {
		t.Fatal("native list writes modified original query records or snapshot receipt")
	}
	snapshot.graphVisualState().focus("n:" + root)
	for _, property := range snapshot.graphVisualState().properties() {
		if property.Name == "values" && !reflect.DeepEqual(property.Value, exact) {
			t.Fatal("later native lists mutated independent snapshot elements")
		}
	}
	read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (a)-[r:NYA_LISTS]->() WHERE elementId(a)=$id RETURN a.values,r.weights,r[$key],a.name,a.exact", Mode: "read", Parameters: map[string]any{"id": root, "key": key}})
	if !reflect.DeepEqual(read.Rows[0][0], graphListValueTest(t, "integer", "[-9223372036854775808,9223372036854775807]")) || !reflect.DeepEqual(read.Rows[0][1], weights) || read.Rows[0][2].Kind != "null" || read.Rows[0][3].Text != "Original 猫" || read.Rows[0][4].Text != "9223372036854775807" {
		t.Fatal("native list operations lost stored types or changed unrelated properties")
	}
	s.Close(d)
	if d.graphPropertyRecords != nil || d.graphPropertyDraft != nil || !snapshot.closed || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("original list document close retained caches or closed replacement session")
	}
}
