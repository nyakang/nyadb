package dataworkspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/locales"
)

func inputGraphWizardTest(t *testing.T, s *State, d *Document, label, value string) {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type(value)
}

func TestNeo4jNativePublicGraphImportFileMappingTypedPreviewCommitAndDatabaseExchange(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, manager, s, active, caps := openNeo4jEntityWorkbench(t, f)
	label, rel := "NyaPublicImport_"+f.Nonce, "NyaPublicImportEdges_"+f.Nonce
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	seed := neo4jDataRun(t, admin, "CREATE (n:$($label) {keep:42}) RETURN n", map[string]any{"label": label}).Rows[0][0].Node.Clone()
	props := neo4jDataRun(t, admin, "RETURN {exact:$exact,bytes:$bytes,empty:'',flag:false,float:-0.0,date:date({year:10000,month:2,day:29}),local_time:localtime('23:59:59.999999999'),local_datetime:localdatetime('-0001-02-28T23:59:59.999999999'),time:time('01:02:03+00:20'),datetime:datetime('2026-01-01T00:00:00Z'),duration:duration({months:-2,days:3,seconds:0,nanoseconds:-1}),point2d:point({srid:4326,x:181,y:-90}),point3d:point({srid:4979,x:1,y:2,z:-0.0})}", map[string]any{"exact": int64(9223372036854775807), "bytes": []byte{0, 255}}).Rows[0][0].Properties
	for _, p := range append([]contract.GraphProperty(nil), props...) {
		if p.Value.Kind != "bytes" {
			props = append(props, contract.GraphProperty{Name: p.Name + "_list", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{p.Value.Clone(), p.Value.Clone()}}})
		}
	}
	bundle := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "source-a", Labels: []string{label, "NyaOther_" + f.Nonce}, Properties: props}, {ElementID: "source-b"}}}
	for _, e := range []struct{ id, start, end string }{{"forward", "source-a", "source-b"}, {"parallel", "source-a", "source-b"}, {"reverse", "source-b", "source-a"}, {"self", "source-a", "source-a"}, {"outside", "source-a", "source-external"}} {
		bundle.Relationships = append(bundle.Relationships, contract.GraphRelationship{ElementID: e.id, StartElementID: e.start, EndElementID: e.end, Type: rel, Properties: props})
	}
	var source bytes.Buffer
	if err := graphimport.Write(&source, bundle, "jsonl"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "portable.graph.jsonl")
	if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("label", label), caps)
	s.Load(ui.Services{}, d, false)
	waitNeo4jData(t, s, d)
	if !d.capabilities.Import || s.runtimeFor(d).PreviewGraphImport == nil || s.runtimeFor(d).SubmitGraphImport == nil || s.runtimeFor(d).ExportGraph == nil {
		t.Fatal("complete graph features not bound to original public document")
	}
	entityTestClick(t, s, d, "导入")
	if !d.transfer.GraphExchange {
		t.Fatal("graph toolbar did not open shared graph wizard")
	}
	d.transfer.Format = "JSONL"
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	entityTestClick(t, s, d, "选择文件")
	settleDocumentTransfer(t, s, d)
	if d.transfer.GraphBundle == nil || !reflect.DeepEqual(*d.transfer.GraphBundle, bundle) {
		t.Fatal("public graph file lost complete values", d.transfer.Err)
	}
	captureNeo4jData(t, s, d, "graph-import-source", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "下一步")
	entityTestClick(t, s, d, "预览图导入")
	if d.transfer.Busy || d.transfer.Err == "" {
		t.Fatal("unmapped external endpoint admitted a preview")
	}
	inputGraphWizardTest(t, s, d, "源节点或外部端点 ID", "source-external")
	inputGraphWizardTest(t, s, d, "目标节点 element ID", seed.ElementID)
	entityTestClick(t, s, d, "添加或替换端点映射")
	captureNeo4jData(t, s, d, "graph-import-mapping", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "预览图导入")
	settleDocumentTransfer(t, s, d)
	if d.transfer.GraphPlan == nil || d.transfer.Step != 2 || d.transfer.GraphSnapshot.Nodes != 2 {
		t.Fatal("public original canonical graph preview missing", d.transfer.Err)
	}
	captureNeo4jData(t, s, d, "graph-import-preview", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	inputGraphWizardTest(t, s, d, "输入目标数据库名以确认", "neo4j")
	entityTestClick(t, s, d, "确认导入完整图")
	settleDocumentTransfer(t, s, d)
	waitNeo4jData(t, s, d)
	if !d.transfer.Completed || d.transfer.Uncertain || d.transfer.GraphResult == nil || d.transfer.GraphResult.NodesCreated != 2 || d.transfer.GraphResult.RelationshipsCreated != 5 {
		t.Fatal("public graph import completion missing", d.transfer.Err)
	}
	d.nativeReceipt.Open = true
	captureNeo4jData(t, s, d, "graph-import-receipt", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
	d.nativeReceipt.Open = false
	var unlabeled string
	for _, id := range d.transfer.GraphResult.Identities {
		if id.Kind == "node" && id.SourceID == "source-b" {
			unlabeled = id.DestinationID
		}
	}
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE elementId(n)=$id DETACH DELETE n", map[string]any{"id": unlabeled})
	actual := neo4jDataRun(t, admin, "MATCH (n) WHERE elementId(n)=$id RETURN n", map[string]any{"id": seed.ElementID}).Rows[0][0].Node
	if !reflect.DeepEqual(*actual, seed) {
		t.Fatal("public endpoint binding overwrote existing node")
	}
	graph, err := s.runtimeFor(d).ExportGraph(context.Background(), d.object)
	if err != nil || len(graph.Nodes) != 3 || len(graph.Relationships) != 5 {
		t.Fatal("public exchange omitted original database entities", err)
	}
	entityTestClick(t, s, d, "关闭")
	entityTestClick(t, s, d, "导出")
	entityTestClick(t, s, d, "完整数据库图交换")
	if !d.transfer.GraphExchange {
		t.Fatal("shared export wizard omitted full database scope")
	}
	output := filepath.Join(t.TempDir(), "database.graph.json")
	d.transfer.ExportPath = output
	captureNeo4jData(t, s, d, "graph-export-database", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
	entityTestClick(t, s, d, "导出完整数据库图")
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Completed {
		t.Fatal("public complete graph file not published", d.transfer.Err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := graphimport.Parse(bytes.NewReader(raw), "json")
	if err != nil || !reflect.DeepEqual(exported, graph) {
		t.Fatal("public atomic graph export changed original scan", err)
	}
}
