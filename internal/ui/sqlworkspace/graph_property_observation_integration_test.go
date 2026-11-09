package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicGraphPropertyNeighborObservationOrderAndContinuedEditing(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaObservation_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Original node',exact:$exact})-[r:NYA_OBSERVATION {name:'Original relationship'}]->(b:$($label) {name:'Other'}) RETURN a,r,b", Parameters: map[string]any{"label": label, "exact": int64(9223372036854775807)}})
	root, relationship := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Relationship.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	if _, err = m.ConnectSession(context.Background(), p, f.Secret); err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
	defer s.CloseAll()
	source := "MATCH (a)-[r:NYA_OBSERVATION]->(b) WHERE elementId(a)=$node RETURN a,r,b"
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
	g := d.graphVisualState()
	d.graphVisualOpen = true
	// A read before saving is historical after the complete native write receipt.
	g.focus("n:" + root)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	firstNeighbors, _ := json.Marshal(g.observations)
	for _, target := range []struct{ key, value string }{{"n:" + root, "First saved node 猫"}, {"r:" + relationship, "First saved relationship"}} {
		nativeGraphPropertyStart(t, s, d, target.key, target.value)
		s.saveGraphProperty(d)
		waitNativeNeighbors(t, s, d)
		if d.graphPropertyDraft != nil || !d.graphPropertyRecords[len(d.graphPropertyRecords)-1].Receipt.Applied {
			t.Fatal("first native save failed", target.key, d.Error())
		}
	}
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting {
		t.Fatal("saving then expanding neighbors misclassified historical values as a current conflict")
	}
	captureGraphVisual(t, s, d, "property-neighbors-saved")
	for index := 0; index < 2; index++ {
		s.verifyGraphProperty(d, index)
		waitNativeNeighbors(t, s, d)
		if !d.graphPropertyRecords[len(d.graphPropertyRecords)-1].Verification {
			t.Fatal("explicit native verification did not retain its own receipt")
		}
	}
	// Both node and directed relation can be explicitly edited again.
	for _, target := range []struct{ key, value string }{{"n:" + root, "Second saved node"}, {"r:" + relationship, "Second saved relationship"}} {
		nativeGraphPropertyStart(t, s, d, target.key, target.value)
		s.saveGraphProperty(d)
		waitNativeNeighbors(t, s, d)
		if d.graphPropertyDraft != nil || !d.graphPropertyRecords[len(d.graphPropertyRecords)-1].Receipt.Applied {
			t.Fatal("continued native editing failed", target.key, d.Error())
		}
	}
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting || !s.pinResult(d) {
		t.Fatal("second save/expansion cannot retain an independent snapshot", d.Error())
	}
	snapshot := d.snapshots[0].Document
	sg := snapshot.graphVisualState()
	captureGraphVisual(t, s, d, "property-neighbors-continued")
	// Later external changes must still block editing; this cannot be fixed by
	// unconditionally clearing conflicts for every entity that has been saved.
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (a)-[r:NYA_OBSERVATION]->() WHERE elementId(a)=$id SET a.name='Later external node',r.name='Later external relationship'", Parameters: map[string]any{"id": root}})
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if !g.node("n:"+root).Conflicting || !g.edge("r:"+relationship).Conflicting || graphNodeTitle(*g.node("n:" + root)) != "Second saved node" {
		t.Fatal("later native neighbor changes were hidden or replaced the explicit observation")
	}
	for _, key := range []string{"n:" + root, "r:" + relationship} {
		g.focus(key)
		for _, property := range g.properties() {
			if property.Name == "name" && s.canEditGraphProperty(d, g, property) {
				t.Fatal("later conflicting native observation allowed editing", key)
			}
		}
	}
	g.focus("n:" + root)
	captureGraphVisual(t, s, d, "property-neighbors-conflict")
	// Verify only the node. The relation must keep its independent conflict.
	s.verifyGraphProperty(d, 4)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || !g.edge("r:"+relationship).Conflicting || graphNodeTitle(*g.node("n:" + root)) != "Later external node" {
		t.Fatal("node verification reconciled another entity or retained historical conflict")
	}
	nativeGraphPropertyStart(t, s, d, "n:"+root, "Third explicit node")
	s.saveGraphProperty(d)
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft != nil || !d.graphPropertyRecords[len(d.graphPropertyRecords)-1].Receipt.Applied {
		t.Fatal("editing after independent native verification failed")
	}
	s.verifyGraphProperty(d, 5)
	waitNativeNeighbors(t, s, d)
	if g.edge("r:" + relationship).Conflicting {
		t.Fatal("directed relation verification retained old conflict")
	}
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting {
		t.Fatal("post-verification neighbor read revived superseded conflicts")
	}
	captureGraphVisual(t, s, d, "property-neighbors-verified")
	after, _ := json.Marshal(d.result)
	oldNeighbors, _ := json.Marshal(g.observations[:1])
	if !bytes.Equal(before, after) || !bytes.Equal(firstNeighbors, oldNeighbors) || d.Editor.Buffer.String() != source || len(g.node("n:"+root).Cells) == 0 || len(g.edge("r:"+relationship).Observations) == 0 {
		t.Fatal("continuous native editing modified original query/neighbor records or lost source links")
	}
	if sg.node("n:"+root).Conflicting || sg.edge("r:"+relationship).Conflicting || graphNodeTitle(*sg.node("n:" + root)) != "Second saved node" || len(sg.observations) != 3 || len(snapshot.graphPropertyRecords) != 6 {
		t.Fatal("later real operations mutated the independent snapshot")
	}
	read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (a)-[r:NYA_OBSERVATION]->() WHERE elementId(a)=$id RETURN a.name,a.exact,r.name", Mode: "read", Parameters: map[string]any{"id": root}})
	if read.Rows[0][0].Text != "Third explicit node" || read.Rows[0][1].Text != "9223372036854775807" || read.Rows[0][2].Text != "Later external relationship" {
		t.Fatal("continuous native editing changed an unrelated property or replayed a relation write")
	}
}
