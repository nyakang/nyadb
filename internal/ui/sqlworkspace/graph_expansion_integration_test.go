package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func waitNativeNeighbors(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for (d.Loading() || d.prepareResults != nil) && time.Now().Before(deadline) {
		s.Consume()
		time.Sleep(time.Millisecond)
	}
	if d.Loading() || d.prepareResults != nil {
		t.Fatal("original neighbor executor did not acknowledge", d.Error())
	}
}

func TestNeo4jNativePublicGraphNeighborsOriginalRecordsSnapshotsBoundsAndGeneration(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaNeighbors_" + f.Nonce + "_猫`\\u0060"
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	properties := map[string]any{"name": "Neighbor 猫", "exact": int64(9223372036854775807), "bytes": []byte{0, 255, 1}, "when": time.Date(2026, 11, 9, 1, 2, 3, 0, time.FixedZone("original", 7200))}
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Root'}), (b:$($label)), (c:$($label) {name:'Second hop'}) SET b=$properties CREATE (a)-[:OUTGOING]->(b), (b)-[:INCOMING]->(a), (a)-[:SELF]->(a), (b)-[:SECOND_HOP]->(c) RETURN a,b,c", Parameters: map[string]any{"label": label, "properties": properties}})
	a, b, cID := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Node.ElementID, created.Rows[0][2].Node.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	handle, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	active := true
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, Active: func(*Document) bool { return active }})
	defer s.CloseAll()
	source := "MATCH (n) WHERE elementId(n)=$node RETURN n"
	d := s.Open(1, source, p.ID, "neo4j", false)
	d.layoutMode = "result"
	params, _ := json.Marshal(map[string]any{"node": a})
	d.graphParameters = string(params)
	s.Prepare(d)
	waitNativeNeighbors(t, s, d)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" || len(d.result.Graph.Rows) != 1 {
		t.Fatal("native original node query failed", d.Error())
	}
	before, _ := json.Marshal(d.result)
	g := d.graphVisualState()
	g.focus("n:" + a)
	d.graphVisualOpen = true
	captureGraphVisual(t, s, d, "neighbors-before")
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1280, 800)
	tt.Frame()
	position, zoom, pan := g.positions["n:"+a], g.zoom, g.pan
	g.pinned["n:"+a] = true
	if err = tt.Click(locales.Text("扩展邻居")); err != nil {
		t.Fatal(err)
	}
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" || len(g.observations) != 1 || !g.observations[0].Accepted || !g.observations[0].Result.Completed || g.observations[0].Limited || g.observations[0].Namespace != d.Namespace() || len(g.topology.Nodes) != 2 || len(g.topology.Edges) != 3 {
		t.Fatal("native qualified neighborhood failed", d.Error())
	}
	if g.positions["n:"+a] != position || g.zoom != zoom || g.pan != pan || !g.pinned["n:"+a] {
		t.Fatal("native expansion reset old layout/camera")
	}
	for _, edge := range g.topology.Edges {
		if len(edge.Cells) != 0 || len(edge.Observations) == 0 {
			t.Fatal("native neighbor edge assigned to original rows")
		}
		if edge.Value.Type == "INCOMING" && (edge.Source != "n:"+b || edge.Target != "n:"+a) {
			t.Fatal("native incoming direction changed")
		}
		if edge.Value.Type == "SELF" && edge.Source != edge.Target {
			t.Fatal("native self-loop changed")
		}
	}
	if len(g.node("n:"+b).Cells) != 0 || g.node("n:"+cID) != nil {
		t.Fatal("neighbor identity or one-hop range fabricated")
	}
	g.focus("n:" + b)
	captureGraphVisual(t, s, d, "neighbors-after")
	if !strings.Contains(g.detailJSON(), "9223372036854775807") || !strings.Contains(g.detailJSON(), "bytes") || !strings.Contains(g.detailJSON(), "datetime") {
		t.Fatal("native expansion narrowed typed properties")
	}
	refs := g.observationRefs()
	if len(refs) == 0 {
		t.Fatal("expanded node has no original neighbor record")
	}
	g.observationIndex = refs[0].Observation
	g.inspectorMode = "expansion"
	g.fieldJSON = ""
	captureGraphVisual(t, s, d, "neighbors-record")
	if !strings.Contains(g.observationDetail(), graphExpansionQuery) || !strings.Contains(g.observationDetail(), "9223372036854775807") {
		t.Fatal("neighbor native receipt/record lost")
	}
	g.inspectorMode = ""
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if len(g.observations) != 2 || g.node("n:"+cID) == nil || len(g.node("n:"+cID).Cells) != 0 {
		t.Fatal("native second explicit hop failed or entered original table")
	}
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snapshot := d.snapshots[0].Document
	sg := snapshot.graphVisualState()
	if s.canExpandGraph(snapshot) || len(sg.observations) != 2 || sg.node("n:"+cID) == nil {
		t.Fatal("native snapshot lost expanded graph or permitted live reads")
	}
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n.name='Updated root' RETURN n", Parameters: map[string]any{"id": a}})
	g.focus("n:" + a)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if !g.node("n:"+a).Conflicting || g.node("n:" + a).Value.Properties[0].Value.Text != "Root" || sg.node("n:"+a).Conflicting || len(sg.observations) != 2 {
		t.Fatal("native re-observation silently merged originals or mutated snapshot")
	}
	var exported bytes.Buffer
	s.SetPersistence(nil, Files{SavePath: func(name string) (string, error) { return name, nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&exported) }}, nil)
	s.exportGraphObservations(d)
	deadline := time.Now().Add(20 * time.Second)
	for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	var records []graphObservation
	if json.Unmarshal(exported.Bytes(), &records) != nil || len(records) != 3 || !records[0].Result.Completed || !strings.Contains(exported.String(), "9223372036854775807") || !strings.Contains(exported.String(), "Updated root") {
		t.Fatal("complete native neighbor JSON export failed", d.Error())
	}
	exported.Reset()
	s.exportGraphImage(d, themePaletteForTests(), "SVG")
	deadline = time.Now().Add(20 * time.Second)
	for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	decoder := xml.NewDecoder(bytes.NewReader(exported.Bytes()))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(exported.String(), "SECOND_HOP") || !strings.Contains(exported.String(), "Second hop") {
		t.Fatal("graph image export omitted expanded neighbors")
	}
	wide := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (root:$($label) {name:'Wide root'}) WITH root UNWIND range(0,204) AS i CREATE (root)-[:WIDE]->(neighbor:$($label) {ordinal:i}) RETURN elementId(root),count(neighbor)", Parameters: map[string]any{"label": label}})
	w := s.Open(2, source, p.ID, "neo4j", false)
	w.layoutMode = "result"
	wp, _ := json.Marshal(map[string]any{"node": wide.Rows[0][0].Text})
	w.graphParameters = string(wp)
	s.Prepare(w)
	waitNativeNeighbors(t, s, w)
	s.Run(nil, w)
	waitNativeNeighbors(t, s, w)
	wg := w.graphVisualState()
	w.graphVisualOpen = true
	wg.focus("n:" + wide.Rows[0][0].Text)
	s.expandGraph(w)
	waitNativeNeighbors(t, s, w)
	if len(wg.observations) != 1 || !wg.observations[0].Accepted || !wg.observations[0].Limited || len(wg.observations[0].Result.Rows) != 200 || len(wg.topology.Edges) != 200 || len(w.result.Graph.Rows) != 1 || w.NeedsRecovery() {
		t.Fatal("native neighbor limit changed original query/completion or hid bounded evidence", w.Error())
	}
	wg.fit = true
	captureGraphVisual(t, s, w, "neighbors-limited")
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) || m.Generation(p.ID) != handle.Generation() {
		t.Fatal("native expansion/export changed original source/table/receipt/publication")
	}
	active = false
	g.focus("n:" + a)
	s.expandGraph(d)
	n := d.graphExpansion
	deadline = time.Now().Add(20 * time.Second)
	for len(n.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(n.results) == 0 {
		t.Fatal("native neighbor result did not park")
	}
	fresh, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	s.Consume()
	if !d.NeedsRecovery() || g.observations[len(g.observations)-1].Accepted || !g.observations[len(g.observations)-1].Result.Completed || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("parked native original generation admitted onto replacement")
	}
	s.Recover(d)
	waitNativeNeighbors(t, s, d)
	if d.NeedsRecovery() || d.result != nil || d.graphVisual != nil || !g.closed || d.Editor.Buffer.String() != source || sg.closed {
		t.Fatal("explicit original recovery replayed a query/expansion or lost independent snapshot", d.Error())
	}
	s.Close(d)
	s.Close(w)
	if !sg.closed || !wg.closed || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("original graph cleanup reached replacement or retained caches")
	}
}

func TestNeo4jNativePublicGraphNeighborsLostCanceledOriginalCloseNeverReplay(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaNeighborFault_" + f.Nonce
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (n:$($label) {name:'Fault root'})-[:NEIGHBOR]->(:$($label) {name:'Fault neighbor'}) RETURN n", Parameters: map[string]any{"label": label}})
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	root := created.Rows[0][0].Node.ElementID
	for _, fault := range []string{"lost", "cancel", "close"} {
		t.Run(fault, func(t *testing.T) {
			relay := newNeo4jRelay(t, f, graphExpansionQuery)
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile = ""
			original.Config.ClientCertFile = ""
			original.Config.ClientKeyFile = ""
			p := neo4jWorkbenchProfile(t, original)
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err = m.ConnectSession(context.Background(), p, f.Secret); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
			defer s.CloseAll()
			source := "MATCH (n) WHERE elementId(n)=$node RETURN n"
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
			g := d.graphVisualState()
			g.focus("n:" + root)
			d.graphVisualOpen = true
			before, _ := json.Marshal(d.result)
			s.expandGraph(d)
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("native original neighbor RUN was not forwarded")
			}
			// The owned server has replied after the exact native query; the relay
			// withholds that response before cancellation/loss/original close.
			select {
			case <-relay.responded:
			case <-time.After(15 * time.Second):
				t.Fatal("owned Neo4j never replied to the original neighbor query")
			}
			switch fault {
			case "lost":
				relay.Close()
			case "cancel":
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("native neighbor cancellation settled before acknowledgement")
				}
			case "close":
				s.Close(d)
			}
			if fault != "close" {
				waitNativeNeighbors(t, s, d)
				after, _ := json.Marshal(d.result)
				if !d.NeedsRecovery() || !bytes.Equal(before, after) || len(g.observations) != 1 || g.observations[0].Accepted || !g.observations[0].Result.Uncertain || g.observations[0].Result.Completed {
					t.Fatal("lost native neighbor receipt replaced original graph or failed recovery", d.Error())
				}
				s.expandGraph(d)
				s.Run(nil, d)
				if d.Loading() {
					t.Fatal("lost neighbor request/original query replayed")
				}
				if fault == "cancel" {
					captureGraphVisual(t, s, d, "neighbors-retired")
				}
			}
			freshProfile := neo4jWorkbenchProfile(t, f)
			fresh, err := m.ConnectSession(context.Background(), freshProfile, f.Secret)
			if err != nil {
				t.Fatal(err)
			}
			if fault != "close" {
				s.Recover(d)
				waitNativeNeighbors(t, s, d)
				if d.NeedsRecovery() || d.result != nil || d.graphVisual != nil || d.Editor.Buffer.String() != source {
					t.Fatal("native neighbor recovery replayed existing operations")
				}
				s.Close(d)
			}
			if !g.closed || relay.dispatches.Load() != 1 || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
				t.Fatal("native original cleanup reached replacement or replayed neighbors")
			}
			// The replacement still performs an independent native query after old close.
			published, _ := m.Session(p.ID)
			provider, e := contract.Require[contract.QueryProvider](published)
			if e != nil {
				t.Fatal(e)
			}
			lease, e := provider.ReserveQuery(99)
			if e != nil {
				t.Fatal(e)
			}
			defer lease.Release()
			r, e := lease.(contract.GraphQueryLease).ExecuteGraph(context.Background(), contract.GraphQueryRequest{Query: "RETURN $id", Database: "neo4j", Parameters: map[string]any{"id": root}, Mode: "read"})
			if e != nil || !r.Completed || r.Rows[0][0].Text != root {
				t.Fatal("replacement lease was revoked by original cleanup", e)
			}
		})
	}
}
