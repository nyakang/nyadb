package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type neighborTestLease struct {
	*graphTestLease
	revoked atomic.Bool
}

func (l *neighborTestLease) CheckQueryLease() error {
	if l.revoked.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func neighborResult() contract.GraphResult {
	a := contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: "later Alice"}}}}
	n := contract.GraphNode{ElementID: "new", Properties: []contract.GraphProperty{{Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}}
	e := contract.GraphRelationship{ElementID: "new-edge", StartElementID: "a", EndElementID: "new", Type: "EXPANDED"}
	return contract.GraphResult{Columns: []string{"n", "r", "m"}, Attempted: true, Completed: true, Summary: contract.GraphSummary{Database: "neo4j", Bookmarks: []string{"original native read receipt"}}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &a}, {Kind: "relationship", Relationship: &e}, {Kind: "node", Node: &n}}}}
}
func neighborhoodDocument(t *testing.T, execute func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error)) (*State, *Document, *neighborTestLease) {
	t.Helper()
	s, d := visualDocument(t)
	s.deps.Connected = func(id int64) bool { return id == 17 }
	lease := &neighborTestLease{graphTestLease: &graphTestLease{testLease: &testLease{}, graph: execute}}
	d.lease = lease
	d.graphVisualOpen = true
	d.graphVisualState().focus("n:a")
	return s, d, lease
}
func TestGraphNeighborhoodOriginalRequestParkedReceiptProvenanceAndBusyGuards(t *testing.T) {
	queries := make(chan contract.GraphQueryRequest, 1)
	s, d, lease := neighborhoodDocument(t, func(_ context.Context, request contract.GraphQueryRequest) (contract.GraphResult, error) {
		queries <- request
		return neighborResult(), nil
	})
	active := true
	s.deps.Active = func(*Document) bool { return active }
	d.graphParameters = "an invalid independent editor parameter draft"
	before, _ := json.Marshal(d.result)
	g := d.graphVisualState()
	g.pinned["n:a"] = true
	g.hidden["n:b"] = true
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1280, 800)
	tt.Frame()
	position, zoom, pan := g.positions["n:a"], g.zoom, g.pan
	if err := tt.Click(locales.Text("扩展邻居")); err != nil {
		t.Fatal(err)
	}
	request := <-queries
	if request.Query != graphExpansionQuery || request.Database != "neo4j" || request.Mode != "read" || !reflect.DeepEqual(request.Parameters, map[string]any{"node": "a", "limit": int64(200)}) || request.MaxRows != 200 || request.MaxBytes > 4<<20 {
		t.Fatal("unqualified, unbounded or editor-dependent expansion", request)
	}
	s.Run(nil, d)
	if !d.Loading() || lease.executions.Load() != 0 || s.pinResult(d) {
		t.Fatal("another native operation or snapshot raced the original expansion")
	}
	active = false
	s.Consume()
	if !d.Loading() || len(g.observations) != 0 {
		t.Fatal("parked neighbor receipt published before activation")
	}
	active = true
	consumeUntil(t, s, func() bool { return !d.Loading() })
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) || len(g.observations) != 1 || !g.observations[0].Accepted || !g.observations[0].Result.Completed || g.observations[0].Namespace != d.Namespace() {
		t.Fatal("expansion replaced the original query/result/receipt")
	}
	if g.node("n:new") == nil || len(g.node("n:new").Cells) != 0 || len(g.node("n:new").Observations) == 0 || !g.node("n:a").Conflicting || g.node("n:a").Value.Properties[0].Value.Text != "Alice <&> 猫" {
		t.Fatal("new neighbor provenance or original conflict evidence lost")
	}
	if g.positions["n:a"] != position || !g.pinned["n:a"] || !g.hidden["n:b"] || g.zoom != zoom || g.pan != pan {
		t.Fatal("neighbor admission reset layout, camera, pin or hidden state")
	}
	g.focus("n:new")
	if len(g.cells()) != 0 || len(g.observationRefs()) == 0 || !d.graphLocate(0, 0) {
		t.Fatal("neighbor rows were linked to original grid cells")
	}
}
func TestGraphNeighborhoodSnapshotsExportsAndReplacementOwnValues(t *testing.T) {
	s, d, _ := neighborhoodDocument(t, func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		return neighborResult(), nil
	})
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	g := d.graphVisualState()
	g.focus("n:new")
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snapshot := d.snapshots[0].Document
	sg := snapshot.graphVisualState()
	if s.canExpandGraph(snapshot) || sg.node("n:new") == nil || len(sg.observations) != 1 || d.snapshots[0].Bytes < int64(g.observationBytes) {
		t.Fatal("snapshot lost expansion observations or permitted live reads")
	}
	started, release, complete := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var exported []byte
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { close(started); <-release; return "neighbors.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = append([]byte(nil), b.Bytes()...)
		close(complete)
		return err
	}}, nil)
	s.exportGraphObservations(d)
	awaitSignal(t, started)
	g.observations[0].Result.Rows[0][2].Node.Properties[0].Value.Text = "changed live value"
	g.positions["n:new"] = ui.Point{X: 99999}
	g.hidden["n:new"] = true
	if sg.observations[0].Result.Rows[0][2].Node.Properties[0].Value.Text != "9223372036854775807" || sg.hidden["n:new"] {
		t.Fatal("snapshot borrowed live expanded values/layout")
	}
	close(release)
	awaitSignal(t, complete)
	var observations []graphObservation
	if err := json.Unmarshal(exported, &observations); err != nil || len(observations) != 1 || observations[0].Result.Rows[0][2].Node.Properties[0].Value.Text != "9223372036854775807" || !observations[0].Result.Completed {
		t.Fatal("export borrowed later values or lost native receipts", err)
	}
	r := visualGraphResult()
	d.setResult(&r)
	if !g.closed || g.observations != nil || sg.closed || sg.node("n:new") == nil {
		t.Fatal("replacement retained live caches or destroyed the independent snapshot")
	}
	d.removeSnapshot(0)
	if !sg.closed || sg.base != nil || sg.observations != nil {
		t.Fatal("removed snapshot retained original or expanded graph values")
	}
}
func TestGraphNeighborhoodCancellationAcknowledgementRecoveryAndNoReplay(t *testing.T) {
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, d, original := neighborhoodDocument(t, func(ctx context.Context, _ contract.GraphQueryRequest) (contract.GraphResult, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		<-ack
		return contract.GraphResult{Attempted: true, Uncertain: true}, errors.Join(ctx.Err(), contract.ErrSQLSessionLost)
	})
	fresh := &neighborTestLease{graphTestLease: &graphTestLease{testLease: &testLease{}, graph: func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		calls.Add(1)
		return neighborResult(), nil
	}}}
	original.replace = func() (Lease, error) { return fresh, nil }
	s.deps.Runtime = func() Runtime { return &testRuntime{} }
	g := d.graphVisualState()
	before, _ := json.Marshal(d.result)
	s.expandGraph(d)
	awaitSignal(t, started)
	s.RequestCancel(d)
	awaitSignal(t, canceled)
	s.Consume()
	s.Recover(d)
	if !d.Loading() || !d.CancelRequested() || calls.Load() != 1 || d.lease != original {
		t.Fatal("cancellation settled before executor acknowledgement")
	}
	close(ack)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) || !d.NeedsRecovery() || !g.observations[0].Result.Uncertain || g.observations[0].Accepted {
		t.Fatal("cancellation lost receipt/old graph or failed to require original recovery")
	}
	s.expandGraph(d)
	s.Run(nil, d)
	if calls.Load() != 1 {
		t.Fatal("failed neighbor request or original query replayed")
	}
	s.Recover(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	if d.NeedsRecovery() || !g.closed || d.graphVisual != nil || calls.Load() != 1 || d.Editor.Buffer.String() != "RETURN original graph" {
		t.Fatal("explicit recovery retained old graph state or replayed native requests")
	}
}
func TestGraphNeighborhoodParkedGenerationRevocationAndLateReplacement(t *testing.T) {
	s, d, lease := neighborhoodDocument(t, func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		return neighborResult(), nil
	})
	active := false
	s.deps.Active = func(*Document) bool { return active }
	s.expandGraph(d)
	n := d.graphExpansion
	deadline := time.Now().Add(3 * time.Second)
	for len(n.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(n.results) == 0 {
		t.Fatal("neighbor worker did not park its original result")
	}
	lease.revoked.Store(true)
	active = true
	s.Consume()
	g := d.graphVisualState()
	if !d.NeedsRecovery() || g.node("n:new") != nil || !g.observations[0].Result.Completed || g.observations[0].Accepted {
		t.Fatal("late original generation published onto a replacement session")
	}
	started, finish := make(chan struct{}), make(chan struct{})
	s2, d2, l2 := neighborhoodDocument(t, func(ctx context.Context, _ contract.GraphQueryRequest) (contract.GraphResult, error) {
		close(started)
		<-ctx.Done()
		close(finish)
		return neighborResult(), nil
	})
	g2 := d2.graphVisualState()
	s2.expandGraph(d2)
	awaitSignal(t, started)
	r := visualGraphResult()
	d2.setResult(&r)
	awaitSignal(t, finish)
	s2.Consume()
	if !g2.closed || d2.Loading() || len(d2.graphVisualState().observations) != 0 || l2.releases != 0 {
		t.Fatal("replacement admitted late graph values or closed a different lease")
	}
	s2.Close(d2)
	if l2.releases != 1 {
		t.Fatal("close did not release the original lease exactly once")
	}
}
func TestGraphNeighborhoodBoundedPayloadRetainsReceiptWithoutChangingOriginalGraph(t *testing.T) {
	_, d := visualDocument(t)
	g := d.graphVisualState()
	before, _ := json.Marshal(g.topology)
	r := neighborResult()
	r.Rows[0][2].Node.Properties[0].Value.Text = strings.Repeat("x", graphExpansionBytes)
	g.addObservation(graphObservation{Result: r, Accepted: true, PayloadRetained: true})
	after, _ := json.Marshal(g.topology)
	if !bytes.Equal(before, after) || g.observationBytes > graphExpansionBytes || g.observations[0].Accepted || g.observations[0].PayloadRetained || !g.observations[0].Result.Completed || g.observations[0].Result.Summary.Bookmarks[0] != "original native read receipt" {
		t.Fatal("display budget discarded native completion or changed original topology")
	}
	if len(graphresult.ExtractTopologyObservations(*d.result.Graph, nil).Nodes) != len(g.topology.Nodes) {
		t.Fatal("bounded expansion fabricated neighbors")
	}
}

func TestGraphNeighborhoodExpandedRecordsCountTowardsSnapshotBudget(t *testing.T) {
	s, d := visualDocument(t)
	g := d.graphVisualState()
	r := neighborResult()
	r.Rows[0][2].Node.Properties[0].Value.Text = strings.Repeat("x", 1<<20)
	for range 6 {
		g.addObservation(graphObservation{Result: r, Accepted: true, PayloadRetained: true})
	}
	if !s.pinResult(d) || !s.pinResult(d) || s.pinResult(d) || len(d.snapshots) != 2 || d.Error() == "" {
		t.Fatal("expanded observations were omitted from the 16 MiB snapshot budget")
	}
}
