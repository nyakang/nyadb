package sqlworkspace

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func visualGraphResult() contract.SQLResult {
	a := contract.GraphNode{ElementID: "a", Labels: []string{"Person"}, Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: "Alice <&> 猫"}}, {Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}}
	b := contract.GraphNode{ElementID: "b", Labels: []string{"Person"}, Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: "Bob"}}}}
	edges := []contract.GraphRelationship{{ElementID: "parallel1", StartElementID: "a", EndElementID: "b", Type: "KNOWS"}, {ElementID: "parallel2", StartElementID: "a", EndElementID: "b", Type: "KNOWS"}, {ElementID: "reverse", StartElementID: "b", EndElementID: "a", Type: "RETURNS"}, {ElementID: "loop", StartElementID: "a", EndElementID: "a", Type: "SELF"}}
	missing := contract.GraphRelationship{ElementID: "missing", StartElementID: "b", EndElementID: "unknown", Type: "UNRETURNED"}
	graph := contract.GraphResult{Columns: []string{"path", "relationship", "scalar"}, Completed: true, Attempted: true, Summary: contract.GraphSummary{Database: "neo4j"}, Rows: [][]contract.GraphValue{{{Kind: "path", Path: &contract.GraphPath{Nodes: []contract.GraphNode{a, b}, Relationships: edges}}, {Kind: "relationship", Relationship: &missing}, {Kind: "string", Text: "plain"}}}}
	return graphresult.Project("RETURN original graph", graph)
}
func visualDocument(t *testing.T) (*State, *Document) {
	t.Helper()
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	d := s.Open(1, "RETURN original graph", 17, "neo4j", false)
	d.commandDialect = "neo4j"
	d.layoutMode = "result"
	r := visualGraphResult()
	d.setResult(&r)
	d.executedSQL = "RETURN original graph"
	return s, d
}
func TestGraphVisualNativeCanvasSelectionDragPinHideSearchAndRecords(t *testing.T) {
	s, d := visualDocument(t)
	before, _ := json.Marshal(d.result)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 720, 520)
	if err := tt.Click(locales.Text("图形视图")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	g := d.graphVisualState()
	rect, ok := tt.Find(locales.Text("Neo4j 图画布"))
	if !ok || rect.W < 150 || rect.H < 80 {
		t.Fatal("graph canvas unreachable", rect)
	}
	p := g.screen(g.positions["n:a"])
	tt.ClickAt(rect.X+p.X, rect.Y+p.Y)
	if g.selected != "n:a" || !strings.Contains(g.detailJSON(), "9223372036854775807") {
		t.Fatal("canvas node selection lost native typed property")
	}
	if err := tt.Click("exact " + locales.Text("完整属性值")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if g.inspectorMode != "field" || !strings.Contains(g.fieldJSON, "9223372036854775807") {
		t.Fatal("complete property value unavailable")
	}
	if err := tt.Click(locales.Text("返回图属性")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	original := g.positions["n:a"]
	tt.Press(rect.X+p.X, rect.Y+p.Y)
	tt.Move(rect.X+p.X+45, rect.Y+p.Y+30)
	tt.Release(rect.X+p.X+45, rect.Y+p.Y+30)
	if g.positions["n:a"] == original {
		t.Fatal("native pointer drag did not move selected node")
	}
	if err := tt.Click(locales.Text("固定节点")); err != nil {
		t.Fatal(err)
	}
	pinned := g.positions["n:a"]
	if err := tt.Click(locales.Text("重新布局")); err != nil || !g.pinned["n:a"] || g.positions["n:a"] != pinned {
		t.Fatal("relayout moved a pinned node", err)
	}
	if err := tt.Click(locales.Text("隐藏所选图元")); err != nil || !g.hidden["n:a"] {
		t.Fatal("hide failed", err)
	}
	for _, e := range g.shape(themePaletteForTests()).Edges {
		if e.Key == "r:loop" || e.Key == "r:parallel1" {
			t.Fatal("hidden node retained incident visible edges")
		}
	}
	if err := tt.Click(locales.Text("恢复隐藏图元")); err != nil || len(g.hidden) != 0 {
		t.Fatal("restore failed", err)
	}
	g.query = "Bob"
	tt.Frame()
	if err := tt.Click(locales.Text("定位匹配")); err != nil || g.selected != "n:b" {
		t.Fatal("search did not focus matching native node", err)
	}
	if !d.graphLocate(0, 0) || g.selected != "n:a" || !d.graphLocate(0, 1) || g.selected != "r:missing" || d.graphLocate(0, 2) {
		t.Fatal("original path/edge/scalar cells incorrectly mapped")
	}
	g.focus("n:a")
	d.graphRecord(graphresult.CellRef{Row: 0, Column: 0})
	tt.Frame()
	if !g.records || d.selectedRow != 0 || d.grid.Cells.Cursor != "sql_0" || d.hiddenResultColumns["sql_0"] || !tt.HasText("path") {
		t.Fatal("original row link did not expose original source cell")
	}
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) || d.NeedsRecovery() {
		t.Fatal("graph interactions changed native result or receipt")
	}
	if root := os.Getenv("NYADB_GRAPH_UNIT_IMAGES"); root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(root, "graph-records-unit.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, tt.Image())
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
}
func themePaletteForTests() theme.Palette {
	var palette theme.Palette
	ui.Render(func(c *ui.Context) { theme.Apply(c); palette = theme.Colors(c) }, 1, 1, 1)
	return palette
}
func TestGraphVisualGeometryDirectionSelfLoopsParallelEdgesAndZoomAnchor(t *testing.T) {
	_, d := visualDocument(t)
	g := d.graphVisualState()
	g.fitView(800, 500)
	lanes := g.edgeLanes()
	first, second := g.edgePoints(*g.edge("r:parallel1"), lanes["r:parallel1"]), g.edgePoints(*g.edge("r:parallel2"), lanes["r:parallel2"])
	if reflect.DeepEqual(first, second) {
		t.Fatal("parallel relationships collapsed into one line")
	}
	reverse := g.edgePoints(*g.edge("r:reverse"), lanes["r:reverse"])
	start := g.screen(g.positions["n:b"])
	if math.Hypot(float64(reverse[0].X-start.X), float64(reverse[0].Y-start.Y)) > float64(max(5, 20*g.zoom)+1) {
		t.Fatal("reverse relationship started at wrong endpoint")
	}
	loop := g.edgePoints(*g.edge("r:loop"), lanes["r:loop"])
	node := g.screen(g.positions["n:a"])
	if len(loop) < 10 || math.Hypot(float64(loop[0].X-node.X), float64(loop[0].Y-node.Y)) < float64(19*g.zoom) {
		t.Fatal("self-loop lost attachment/direction")
	}
	anchor := ui.Point{X: 130, Y: 210}
	before := g.world(anchor)
	g.zoomAt(1.2, anchor)
	after := g.world(anchor)
	if math.Hypot(float64(before.X-after.X), float64(before.Y-after.Y)) > .001 {
		t.Fatal("zoom moved the world point under pointer")
	}
	position := g.positions["n:a"]
	p := g.screen(position)
	g.input(ui.InputEvent{Kind: ui.InputPointerDown, X: p.X, Y: p.Y, Button: 0})
	g.input(ui.InputEvent{Kind: ui.InputPointerMove, X: p.X + 30, Y: p.Y + 20})
	g.input(ui.InputEvent{Kind: ui.InputPointerUp, Canceled: true})
	if g.dragging || g.positions["n:a"] != position {
		t.Fatal("canceled pointer drag committed a new position")
	}
	if g.input(ui.InputEvent{Kind: ui.InputPointerMove, X: float32(math.NaN())}) || g.input(ui.InputEvent{Kind: ui.InputPointerDown, X: 1e30}) {
		t.Fatal("unbounded/nonfinite input was accepted")
	}
}
func TestGraphVisualPropertyColumnsRetainOriginalNodeProvenance(t *testing.T) {
	s := New(Dependencies{})
	defer s.CloseAll()
	d := s.Open(1, "RETURN n", 17, "neo4j", false)
	d.commandDialect = "neo4j"
	d.setResult(projectedNodeResult("integer"))
	if !d.graphLocate(0, 1) || d.graphVisualState().selected != "n:original" || !reflect.DeepEqual(selectedResult(d).OriginColumns, []int{0, 0, 0}) {
		t.Fatal("expanded property column could not locate its original native node")
	}
	s.openCachedExport(d)
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	d.result.Sets[0].OriginColumns[1] = 99
	if snap.result.Sets[0].OriginColumns[1] != 0 || d.cachedExport.Cached.OriginColumns[1] != 0 {
		t.Fatal("snapshot/export borrowed native column provenance")
	}
}

func TestGraphVisualSnapshotsReplacementCloseAndOriginalProfileIsolation(t *testing.T) {
	s, d := visualDocument(t)
	g := d.graphVisualState()
	g.fitView(700, 400)
	g.selectKey("n:a")
	g.hidden["r:loop"] = true
	g.pinned["n:a"] = true
	g.records = true
	d.graphVisualOpen = true
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	sg := snap.graphVisualState()
	if snap.ProfileID() != 17 || sg == g || !sg.hidden["r:loop"] || !sg.pinned["n:a"] || !snap.graphVisualOpen || sg.pan != g.pan || sg.zoom != g.zoom {
		t.Fatal("graph snapshot reading state or original profile was lost")
	}
	g.hidden["r:loop"] = false
	g.positions["n:a"] = ui.Point{X: 999}
	g.topology.Nodes[0].Value.Properties[0].Value.Text = "changed live cache"
	if !sg.hidden["r:loop"] || sg.positions["n:a"].X == 999 || sg.topology.Nodes[0].Value.Properties[0].Value.Text != "Alice <&> 猫" {
		t.Fatal("snapshot borrowed live reading maps or native cache")
	}
	r := visualGraphResult()
	d.setResult(&r)
	if !g.closed || g.topology.Nodes != nil || g.positions != nil || d.graphVisual != nil || d.graphVisualOpen {
		t.Fatal("replacement retained old graph caches or interaction callbacks")
	}
	other := s.Open(2, "RETURN original graph", 18, "neo4j", false)
	other.commandDialect = "neo4j"
	r = visualGraphResult()
	other.setResult(&r)
	og := other.graphVisualState()
	og.hidden["n:a"] = true
	if d.graphVisualState().hidden["n:a"] {
		t.Fatal("same element IDs mixed profile reading state")
	}
	d.removeSnapshot(0)
	if !sg.closed || sg.topology.Nodes != nil || snap.graphVisual != nil {
		t.Fatal("snapshot close retained graph state")
	}
	live := d.graphVisualState()
	s.DisconnectProfile(17)
	if !live.closed || d.graphVisual != nil || d.result != nil || d.text != "RETURN original graph" || og.closed {
		t.Fatal("disconnect leaked graph cache or affected another profile")
	}
	s.Close(other)
	if !og.closed || og.topology.Nodes != nil {
		t.Fatal("terminal close leaked graph cache")
	}
}
func TestGraphVisualPNGAndSVGExportCaptureImmutableGeometryAndEscapeLabels(t *testing.T) {
	s, d := visualDocument(t)
	g := d.graphVisualState()
	g.fitView(700, 400)
	beforePan, beforeZoom := g.pan, g.zoom
	var output bytes.Buffer
	gate := make(chan struct{})
	started := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(name string) (string, error) { close(started); <-gate; return name, nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&output) }}, nil)
	s.exportGraphImage(d, themePaletteForTests(), "SVG")
	<-started
	g.hidden["n:a"] = true
	g.positions["n:a"] = ui.Point{X: 999999}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	decoder := xml.NewDecoder(bytes.NewReader(output.Bytes()))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("invalid escaped graph SVG", err)
		}
	}
	if !strings.Contains(output.String(), "Alice &lt;&amp;&gt; 猫") || !strings.Contains(output.String(), "r:parallel1") || g.pan != beforePan || g.zoom != beforeZoom {
		t.Fatal("SVG export borrowed later hidden/camera state or lost escaped identity")
	}
	output.Reset()
	s.SetPersistence(nil, Files{SavePath: func(name string) (string, error) { return name, nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&output) }}, nil)
	s.exportGraphImage(d, themePaletteForTests(), "PNG")
	deadline = time.Now().Add(15 * time.Second)
	for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	config, err := png.DecodeConfig(bytes.NewReader(output.Bytes()))
	if err != nil || config.Width != 2560 || config.Height != 1600 {
		t.Fatal("graph image export lost bounded high resolution", config, err)
	}
}
