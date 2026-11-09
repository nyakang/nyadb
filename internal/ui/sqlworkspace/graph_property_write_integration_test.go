package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func captureGraphProperty(t *testing.T, s *State, d *Document, state string) {
	t.Helper()
	root := os.Getenv("NYADB_NEO4J_PUBLIC_IMAGES")
	if root == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			label := locales.Text("编辑图属性")
			if d.graphPropertyDraft != nil && d.graphPropertyRecord < 0 {
				if d.graphPropertyDraft.create {
					label = locales.Text("新增图属性")
				}
				if d.graphPropertyDraft.remove {
					label = locales.Text("移除图属性")
				}
			} else if d.graphPropertyOpen {
				label = locales.Text("属性操作原生回执")
			}
			if state != "property-saved" && state != "presence-added" && !tt.HasText(label) {
				t.Fatal("native graph property flow not visible", state, language)
			}
			if d.graphPropertyOpen {
				if rect, ok := tt.Find(locales.Text("关闭")); !ok || rect.H < 10 || rect.Y+rect.H > float32(size[1])-10 {
					t.Fatal("native property dialog close button outside the window", state, language, rect)
				}
			}
			folder := filepath.Join(root, "graph-property", language)
			if e := os.MkdirAll(folder, 0700); e != nil {
				t.Fatal(e)
			}
			file, e := os.OpenFile(filepath.Join(folder, fmt.Sprintf("%s-%dx%d.png", state, size[0], size[1])), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if e != nil || closeErr != nil {
				t.Fatal(e, closeErr)
			}
		}
	}
}
func nativeGraphPropertyStart(t *testing.T, s *State, d *Document, key, text string) {
	t.Helper()
	g := d.graphVisualState()
	g.focus(key)
	for _, p := range g.properties() {
		if p.Name == "name" {
			s.openGraphPropertyDraft(d, g, p)
			break
		}
	}
	if d.graphPropertyDraft == nil {
		t.Fatal("native qualified scalar editing unavailable")
	}
	d.graphPropertyDraft.text = text
	s.previewGraphProperty(d)
	if d.graphPropertyDraft.preview == nil {
		t.Fatal("native property preview unavailable", d.graphPropertyDraft.error)
	}
}
func TestNeo4jNativePublicGraphPropertyWritesOriginalRecordsSnapshotsConflictsAndVerification(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, e := neo4j.Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	label := "NyaProperty_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Original 猫',exact:$exact})-[r:NYA_EDIT {name:'Original relationship'}]->(b:$($label) {name:'Other'}) RETURN a,r,b", Parameters: map[string]any{"label": label, "exact": int64(9223372036854775807)}})
	root, relationship := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Relationship.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	if _, e = m.ConnectSession(context.Background(), p, f.Secret); e != nil {
		t.Fatal(e)
	}
	runtime := EngineRuntime(m.Session)
	active := true
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, Active: func(*Document) bool { return active }})
	defer s.CloseAll()
	source := "MATCH (a)-[r:NYA_EDIT]->(b) WHERE elementId(a)=$node RETURN a,r,b"
	d := s.Open(1, source, p.ID, "neo4j", false)
	d.layoutMode = "result"
	params, _ := json.Marshal(map[string]any{"node": root})
	d.graphParameters = string(params)
	s.Prepare(d)
	waitNativeNeighbors(t, s, d)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	before, _ := json.Marshal(d.result)
	d.graphVisualOpen = true
	g := d.graphVisualState()
	g.focus("n:" + root)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	position, zoom, pan := g.positions["n:"+root], g.zoom, g.pan
	if e = tt.Click("name " + locales.Text("编辑图属性")); e != nil {
		t.Fatal(e)
	}
	captureGraphProperty(t, s, d, "property-edit")
	d.graphPropertyDraft.text = "Saved 猫 ' \\ `"
	s.previewGraphProperty(d)
	captureGraphProperty(t, s, d, "property-preview")
	tt.Frame()
	if e = tt.Click(locales.Text("确认保存图属性")); e != nil {
		t.Fatal(e)
	}
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" || d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 1 || !d.graphPropertyRecords[0].Receipt.Applied || !d.graphPropertyRecords[0].Receipt.Result.Completed || graphNodeTitle(*g.node("n:" + root)) != "Saved 猫 ' \\ `" {
		t.Fatal("native original property save failed", d.Error())
	}
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) {
		t.Fatal("native property save modified original Cypher result")
	}
	tt.Frame()
	if g.positions["n:"+root] != position || g.zoom != zoom || g.pan != pan {
		t.Fatal("native property save reset existing camera/layout")
	}
	d.graphPropertyOpen = false
	captureGraphProperty(t, s, d, "property-saved")
	d.graphPropertyOpen = true
	d.graphPropertyRecord = 0
	captureGraphProperty(t, s, d, "property-receipt")
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	snapOriginal, _ := json.Marshal(snap.result)
	if len(snap.graphPropertyRecords) != 1 || snap.graphPropertyRecords[0].Receipt.Result.Rows[0][3].Node.Properties[0].Value.Kind == "" {
		t.Fatal("native snapshot lost full entity/write receipt")
	}
	var exported []byte
	exportDone := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return "properties.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = append([]byte(nil), b.Bytes()...)
		close(exportDone)
		return err
	}}, nil)
	s.exportGraphPropertyRecords(snap)
	<-exportDone
	var records []graphPropertyRecord
	if json.Unmarshal(exported, &records) != nil || len(records) != 1 || !records[0].Receipt.Applied || records[0].Request.After.Text != "Saved 猫 ' \\ `" {
		t.Fatal("native independent JSON export lost typed request/receipt")
	}
	// A different directed entity uses its own native identity and saved scalar.
	d.graphPropertyOpen = false
	nativeGraphPropertyStart(t, s, d, "r:"+relationship, "Saved relationship")
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Receipt.Applied || g.edge("r:" + relationship).Value.Properties[0].Value.Text != "Saved relationship" {
		t.Fatal("native directed relationship write failed")
	}
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n.name='Concurrent'", Parameters: map[string]any{"id": root}})
	d.graphPropertyOpen = false
	nativeGraphPropertyStart(t, s, d, "n:"+root, "Rejected draft")
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 3 || !d.graphPropertyRecords[2].Receipt.Conflict || d.graphPropertyRecords[2].Receipt.Applied || d.graphPropertyDraft == nil || d.graphPropertyDraft.text != "Rejected draft" {
		t.Fatal("native conflict overwrote server value or lost draft")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, "property-conflict")
	s.verifyGraphProperty(d, 2)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 4 || !d.graphPropertyRecords[3].Verification || d.graphPropertyRecords[3].Receipt.Applied {
		t.Fatal("native readback fabricated write confirmation")
	}
	d.graphPropertyRecord = 3
	captureGraphProperty(t, s, d, "property-verification")
	read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n.name AS name,n.exact AS exact", Mode: "read", Parameters: map[string]any{"id": root}})
	if read.Rows[0][0].Text != "Concurrent" || read.Rows[0][1].Text != "9223372036854775807" {
		t.Fatal("native compare/save changed unrelated typed property")
	}
	// Park an actually completed write before replacing its public generation.
	d.graphPropertyDraft = nil
	d.graphPropertyOpen = false
	nativeGraphPropertyStart(t, s, d, "n:"+root, "Parked confirmed")
	s.saveGraphProperty(d)
	active = false
	deadline := time.Now().Add(20 * time.Second)
	for len(d.graphPropertyPending.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.graphPropertyPending.results) == 0 {
		t.Fatal("original native property completion was not buffered")
	}
	s.Consume()
	if len(d.graphPropertyRecords) != 4 || !d.Loading() {
		t.Fatal("parked native property result published")
	}
	fresh, e := m.ConnectSession(context.Background(), p, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	active = true
	waitNativeNeighbors(t, s, d)
	if !d.NeedsRecovery() || len(d.graphPropertyRecords) != 5 || !d.graphPropertyRecords[4].Receipt.Applied || !d.graphPropertyRecords[4].Receipt.Result.Completed || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("parked stale publication lost actual receipt or reached replacement graph")
	}
	s.Recover(d)
	waitNativeNeighbors(t, s, d)
	if d.NeedsRecovery() || d.result != nil || d.graphVisual != nil || d.Editor.Buffer.String() != source || len(d.graphPropertyRecords) != 5 || d.graphPropertyDraft == nil {
		t.Fatal("property recovery discarded independent evidence or replayed query")
	}
	s.saveGraphProperty(d)
	if d.Loading() {
		t.Fatal("original attempted property draft replayed after recovery")
	}
	s.verifyGraphProperty(d, 4)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 6 || !d.graphPropertyRecords[5].Verification || d.graphPropertyRecords[5].Receipt.Result.Rows[0][0].Node == nil {
		t.Fatal("explicit post-recovery property readback failed")
	}
	after, _ = json.Marshal(snap.result)
	if !bytes.Equal(after, snapOriginal) || snap.graphPropertyRecords[0].Request.After.Text != "Saved 猫 ' \\ `" || graphNodeTitle(*snap.graphVisualState().node("n:" + root)) != "Saved 猫 ' \\ `" {
		t.Fatal("later native writes changed independent snapshot")
	}
	s.Close(d)
	if d.graphPropertyRecords != nil || d.graphPropertyDraft != nil || !snap.closed || !m.IsConnected(p.ID) {
		t.Fatal("terminal property cleanup reached replacement or retained caches")
	}
}

func TestNeo4jNativePublicGraphPropertyLostCanceledOriginalCloseAndRecoveryNeverReplay(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, e := neo4j.Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	label := "NyaPropertyFault_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	for _, scenario := range []struct{ operation, fault string }{{"edit", "lost"}, {"edit", "cancel"}, {"edit", "close"}, {"add", "lost"}, {"add", "cancel"}, {"add", "close"}, {"remove", "lost"}, {"remove", "cancel"}, {"remove", "close"}, {"list_edit", "lost"}, {"list_edit", "cancel"}, {"list_edit", "close"}, {"list_add", "lost"}, {"list_add", "cancel"}, {"list_add", "close"}, {"list_remove", "lost"}, {"list_remove", "cancel"}, {"list_remove", "close"}} {
		fault := scenario.fault
		t.Run(scenario.operation+"/"+fault, func(t *testing.T) {
			created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (n:$($label) {name:'Before',values:[0]}) RETURN n", Parameters: map[string]any{"label": label}})
			root := created.Rows[0][0].Node.ElementID
			relay := newNeo4jRelay(t, f, "SET e[$key]=e[$key]")
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile = ""
			original.Config.ClientCertFile = ""
			original.Config.ClientKeyFile = ""
			p := neo4jWorkbenchProfile(t, original)
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, e = m.ConnectSession(context.Background(), p, f.Secret); e != nil {
				t.Fatal(e)
			}
			runtime := EngineRuntime(m.Session)
			s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
			defer s.CloseAll()
			source := "MATCH (n) WHERE elementId(n)=$id RETURN n"
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
			d.graphVisualOpen = true
			before, _ := json.Marshal(d.result)
			property := "name"
			g := d.graphVisualState()
			g.focus("n:" + root)
			switch scenario.operation {
			case "edit":
				nativeGraphPropertyStart(t, s, d, "n:"+root, "Committed "+fault)
			case "add", "list_add":
				property = "new key"
				s.openGraphPropertyCreation(d, g)
				d.graphPropertyDraft.name, d.graphPropertyDraft.text = property, "Committed "+fault
				if scenario.operation == "list_add" {
					d.graphPropertyDraft.kind, d.graphPropertyDraft.text = "list:integer", "[-9223372036854775808,9223372036854775807]"
				}
			case "remove", "list_edit", "list_remove":
				if strings.HasPrefix(scenario.operation, "list_") {
					property = "values"
				}
				for _, p := range g.properties() {
					if p.Name == property {
						if scenario.operation == "list_edit" {
							s.openGraphPropertyDraft(d, g, p)
							d.graphPropertyDraft.text = "[-9223372036854775808,9223372036854775807]"
						} else {
							s.openGraphPropertyRemoval(d, g, p)
						}
						break
					}
				}
			}
			s.previewGraphProperty(d)
			if d.graphPropertyDraft.preview == nil {
				t.Fatal("native presence fault request was not previewed")
			}
			committedValue := d.graphPropertyDraft.preview.After.Clone()
			s.saveGraphProperty(d)
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("native property RUN not forwarded")
			}
			select {
			case <-relay.responded:
			case <-time.After(15 * time.Second):
				t.Fatal("native server did not respond to property write")
			}
			// Observe the actual committed replacement through a separate native client.
			committed := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				r := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n[$key] AS value", Mode: "read", Parameters: map[string]any{"id": root, "key": property}})
				if len(r.Rows) == 1 && reflect.DeepEqual(r.Rows[0][0], committedValue) {
					committed = true
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !committed {
				t.Fatal("native write commit was not externally observed before response fault")
			}
			switch fault {
			case "lost":
				relay.Close()
			case "cancel":
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("native property cancel settled before executor confirmation")
				}
			case "close":
				s.Close(d)
			}
			if fault != "close" {
				waitNativeNeighbors(t, s, d)
				after, _ := json.Marshal(d.result)
				if !d.NeedsRecovery() || !bytes.Equal(before, after) || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || len(d.graphPropertyRecords) != 1 || d.graphPropertyRecords[0].Receipt.Applied || !d.graphPropertyRecords[0].Receipt.Result.Uncertain || d.graphPropertyRecords[0].Receipt.Result.Completed {
					t.Fatal("lost original property response discarded evidence or fabricated rollback", d.Error())
				}
				if fault == "cancel" && scenario.operation == "edit" {
					d.graphPropertyRecord = -1
					captureGraphProperty(t, s, d, "property-retired")
				}
				s.saveGraphProperty(d)
				s.Run(nil, d)
				if d.Loading() {
					t.Fatal("uncertain property operation replayed")
				}
			}
			distinct := contract.GraphValue{Kind: "string", Text: "Distinct after fault"}
			var distinctParameter any = "Distinct after fault"
			if strings.HasPrefix(scenario.operation, "list_") {
				distinctParameter = []int64{9007199254740993}
				distinct = graphListValueTest(t, "integer", "[9007199254740993]")
			}
			neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n[$key]=$value", Parameters: map[string]any{"id": root, "key": property, "value": distinctParameter}})
			freshProfile := neo4jWorkbenchProfile(t, f)
			fresh, e := m.ConnectSession(context.Background(), freshProfile, f.Secret)
			if e != nil {
				t.Fatal(e)
			}
			if fault != "close" {
				s.Recover(d)
				waitNativeNeighbors(t, s, d)
				if d.result != nil || d.graphPropertyDraft == nil || len(d.graphPropertyRecords) != 1 {
					t.Fatal("native property recovery lost original evidence or replayed")
				}
				s.verifyGraphProperty(d, 0)
				waitNativeNeighbors(t, s, d)
				if len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Verification || !d.graphPropertyRecords[0].Receipt.Result.Uncertain || !d.graphPropertyDraft.unresolved {
					t.Fatal("explicit readback supplied a missing write completion")
				}
				s.Close(d)
			}
			read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n[$key] AS value", Mode: "read", Parameters: map[string]any{"id": root, "key": property}})
			if !reflect.DeepEqual(read.Rows[0][0], distinct) || relay.dispatches.Load() != 1 || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
				t.Fatal("native property recovery/old close replayed or reached replacement")
			}
		})
	}
}
