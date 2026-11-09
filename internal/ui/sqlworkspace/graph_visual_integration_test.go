package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
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

func captureGraphVisual(t *testing.T, s *State, d *Document, state string) {
	t.Helper()
	root := os.Getenv("NYADB_NEO4J_PUBLIC_IMAGES")
	if root == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, size[0], size[1])
			tt.Frame()
			if rect, ok := tt.Find(locales.Text("Neo4j 图画布")); !ok || rect.W < 150 || rect.H < 80 || rect.Y+rect.H > float32(size[1])+1 {
				t.Fatal("native graph canvas not visible", state, language, rect)
			}
			if state == "graph-bound" && !tt.HasText(locales.Text("图形展示达到上限；完整保留结果仍可查看和导出")) {
				t.Fatal("graph bound silent", language)
			}
			if state == "property-neighbors-conflict" && !tt.HasText(locales.Text("后续邻居读取与保存或核对时的图元不同；显示保存或核对时的观察")) {
				t.Fatal("later neighbor conflict did not identify the displayed native observation", language)
			}
			if state == "graph-missing-endpoint" && !tt.HasText(locales.Text("端点节点未包含在保留结果中")) {
				t.Fatal("missing endpoint represented as returned native node")
			}
			if state == "graph-node" && !strings.Contains(d.graphVisualState().detailJSON(), "9223372036854775807") {
				t.Fatal("native graph inspector narrowed integer")
			}
			if state == "graph-node" && language == "en" && !tt.HasText("Properties · 6") {
				t.Fatal("graph property heading was not translated")
			}
			folder := filepath.Join(root, "graph-view", language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(filepath.Join(folder, fmt.Sprintf("%s-%dx%d.png", state, size[0], size[1])), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
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
}

func TestNeo4jNativePublicCypherGraphViewOriginalRecordsSnapshotsImagesAndCleanup(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaGraphView_" + f.Nonce + "_猫`\\u0060"
	wide := label + "_wide"
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE any(label IN labels(n) WHERE label IN $labels) DETACH DELETE n", Parameters: map[string]any{"labels": []string{label, wide}}})
	properties := map[string]any{"name": "Alice <&> 猫", "exact": int64(9223372036854775807), "bytes": []byte{0, 255, 1}, "when": time.Date(2026, 7, 3, 1, 2, 3, 0, time.FixedZone("original", 7200)), "list": []int64{-9223372036854775808, 9223372036854775807}}
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label)), (b:$($label)), (c:$($label)) SET a=$properties, a.ordinal=0, b.name='Bob', b.ordinal=1, c.name='Carol', c.ordinal=2 CREATE (a)-[r:KNOWS]->(b), (a)-[s:KNOWS]->(b), (b)-[t:RETURNS]->(a), (a)-[u:SELF]->(a), (b)-[v:UNRETURNED]->(c) RETURN a,b,c,r,s,t,u,v", Parameters: map[string]any{"label": label, "properties": properties}})
	aID, bID, cID := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Node.ElementID, created.Rows[0][2].Node.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	handle, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
	defer s.CloseAll()
	query := "MATCH p=(a)-[r]->(b) WHERE $label IN labels(a) AND $label IN labels(b) RETURN a,r,b,p,[p,r] AS nested,{path:p} AS mapped ORDER BY elementId(r)"
	d := s.Open(1, query, p.ID, "neo4j", false)
	d.layoutMode = "result"
	parameters, _ := json.Marshal(map[string]any{"label": label})
	d.graphParameters = string(parameters)
	wait := func(target *Document) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for (target.Loading() || target.prepareResults != nil) && time.Now().Before(deadline) {
			s.Consume()
			time.Sleep(time.Millisecond)
		}
		if target.Loading() || target.prepareResults != nil || target.Error() != "" {
			t.Fatal("native graph query failed", target.Error())
		}
	}
	s.Prepare(d)
	wait(d)
	s.Run(nil, d)
	wait(d)
	before, _ := json.Marshal(d.result.Graph)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1280, 800)
	if err = tt.Click(locales.Text("图形视图")); err != nil {
		t.Fatal(err)
	}
	g := d.graphVisualState()
	if len(g.topology.Nodes) != 3 || len(g.topology.Edges) != 5 || g.topology.Bounded || g.topology.Unidentified {
		t.Fatal("native topology collapsed path/list/map/parallel/self-loop")
	}
	for _, n := range g.topology.Nodes {
		if n.Missing || n.Conflicting {
			t.Fatal("returned native node was marked missing/conflicting")
		}
	}
	captureGraphVisual(t, s, d, "graph-overview")
	g.focus("n:" + aID)
	captureGraphVisual(t, s, d, "graph-node")
	if !strings.Contains(g.detailJSON(), "9223372036854775807") || !strings.Contains(g.detailJSON(), "bytes") || !strings.Contains(g.detailJSON(), "datetime") {
		t.Fatal("native inspector lost complete typed values")
	}
	tt.Frame()
	if err = tt.Click("exact " + locales.Text("完整属性值")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if g.inspectorMode != "field" || !strings.Contains(g.fieldJSON, "9223372036854775807") {
		t.Fatal("native complete property inspector lost precision")
	}
	if err = tt.Click(locales.Text("返回图属性")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err = tt.Click(locales.Text("图元原生 JSON")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.Text("当前图元的原生 JSON")) {
		t.Fatal("native entity JSON unavailable")
	}
	if err = tt.Click(locales.Text("返回图属性")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var edgeKey string
	for _, e := range g.topology.Edges {
		if e.Value.Type == "RETURNS" {
			edgeKey = e.Key
			if e.Source != "n:"+bID || e.Target != "n:"+aID {
				t.Fatal("native edge direction changed")
			}
		}
	}
	g.focus(edgeKey)
	captureGraphVisual(t, s, d, "graph-relationship")
	refs := g.cells()
	if len(refs) == 0 {
		t.Fatal("native edge had no original cell reference")
	}
	d.graphRecord(refs[0])
	captureGraphVisual(t, s, d, "graph-records")
	if !g.records || d.selectedRow != refs[0].Row || d.result.Steps[0].SQL != query || m.Generation(p.ID) != handle.Generation() || d.NeedsRecovery() {
		t.Fatal("graph browsing changed original query or session")
	}
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	sg := snap.graphVisualState()
	originalPosition := sg.positions["n:"+aID]
	g.hidden["n:"+aID] = true
	g.positions["n:"+aID] = ui.Point{X: 9999}
	if sg.hidden["n:"+aID] || sg.positions["n:"+aID] != originalPosition {
		t.Fatal("native snapshot borrowed live graph positions")
	}
	var imageData bytes.Buffer
	s.SetPersistence(nil, Files{SavePath: func(name string) (string, error) { return name, nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&imageData) }}, nil)
	waitIO := func() {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
			s.ConsumeDocuments(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if len(s.persistence.jobs) > 0 || d.Error() != "" {
			t.Fatal("graph image export did not settle", d.Error())
		}
	}
	g.positions["n:"+aID] = originalPosition
	g.resetHidden()
	s.exportGraphImage(d, themePaletteForTests(), "SVG")
	waitIO()
	decoder := xml.NewDecoder(bytes.NewReader(imageData.Bytes()))
	for {
		_, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	if !strings.Contains(imageData.String(), "Alice &lt;&amp;&gt; 猫") || !strings.Contains(imageData.String(), "SELF") {
		t.Fatal("native graph image lost escaped labels or relationships")
	}
	imageData.Reset()
	s.exportGraphImage(d, themePaletteForTests(), "PNG")
	waitIO()
	image, err := png.DecodeConfig(bytes.NewReader(imageData.Bytes()))
	if err != nil || image.Width != 2560 || image.Height != 1600 {
		t.Fatal("native graph PNG failed", image, err)
	}
	missingQuery := "MATCH (b)-[r:UNRETURNED]->(c) WHERE $label IN labels(b) RETURN r"
	missing := s.Open(2, missingQuery, p.ID, "neo4j", false)
	missing.layoutMode = "result"
	missing.graphParameters = string(parameters)
	s.Prepare(missing)
	wait(missing)
	s.Run(nil, missing)
	wait(missing)
	mg := missing.graphVisualState()
	missing.graphVisualOpen = true
	mg.focus("n:" + cID)
	if !mg.node("n:"+cID).Missing || !mg.node("n:"+bID).Missing || len(mg.node("n:"+cID).Value.Properties) != 0 {
		t.Fatal("unreturned endpoint was fabricated")
	}
	captureGraphVisual(t, s, missing, "graph-missing-endpoint")
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "UNWIND range(0,1000) AS i CREATE (n:$($label)) SET n.ordinal=i RETURN count(n)", Parameters: map[string]any{"label": wide}})
	wideQuery := "MATCH (n) WHERE $label IN labels(n) WITH n ORDER BY n.ordinal RETURN collect(n) AS nodes"
	w := s.Open(3, wideQuery, p.ID, "neo4j", false)
	w.layoutMode = "result"
	wideParameters, _ := json.Marshal(map[string]any{"label": wide})
	w.graphParameters = string(wideParameters)
	s.Prepare(w)
	wait(w)
	s.Run(nil, w)
	wait(w)
	w.graphVisualOpen = true
	wg := w.graphVisualState()
	if !wg.topology.Bounded || len(wg.topology.Nodes) != 1000 || len(w.result.Graph.Rows) != 1 || len(w.result.Graph.Rows[0][0].Values) != 1001 || !w.result.Graph.Completed || w.result.Graph.Truncated || w.result.Steps[0].SQL != wideQuery || w.NeedsRecovery() {
		t.Fatal("bounded graph display changed native completion, source or retained values")
	}

	captureGraphVisual(t, s, w, "graph-bound")
	after, _ := json.Marshal(d.result.Graph)
	if !bytes.Equal(before, after) {
		t.Fatal("native graph browsing/export changed original graph")
	}
	s.Close(d)
	if !g.closed || g.topology.Nodes != nil || !sg.closed || sg.positions != nil || m.Generation(p.ID) != handle.Generation() {
		t.Fatal("native graph close retained caches or closed published session")
	}
	s.Close(missing)
	s.Close(w)
	if !mg.closed || !wg.closed {
		t.Fatal("terminal graph close leaked bounded caches")
	}
}
