package neo4j

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

func graphImportPropertiesTest(t *testing.T) []Property {
	t.Helper()
	props := []Property{{Name: "exact", Value: Value{Kind: "integer", Text: "9223372036854775807"}}, {Name: "bytes", Value: Value{Kind: "bytes", Text: "AP8="}}, {Name: "float", Value: Value{Kind: "float", Text: "-0"}}, {Name: "empty", Value: Value{Kind: "string"}}, {Name: "flag", Value: Value{Kind: "boolean", Text: "false"}}, {Name: "empty_list", Value: Value{Kind: "list"}}}
	for _, sample := range []struct{ kind, text string }{
		{"date", "10000-02-29"}, {"local_time", "23:59:59.999999999"}, {"local_datetime", "-0001-02-28T23:59:59.999999999"},
		{"time", `{"value":"01:02:03+00:20","offset_seconds":1234}`}, {"datetime", `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`}, {"point2d", `{"srid":4326,"x":181,"y":-90}`}, {"point3d", `{"srid":4979,"x":1,"y":2,"z":-0}`},
	} {
		v := extendedPropertyTest(t, sample.kind, sample.text)
		props = append(props, Property{Name: sample.kind, Value: v}, Property{Name: sample.kind + "_list", Value: Value{Kind: "list", Values: []Value{v.Clone(), v.Clone()}}})
	}
	return props
}

func graphImportPreviewTest(t *testing.T, l *dataLease, r contract.GraphImportRequest) contract.GraphImportPlan {
	t.Helper()
	p, err := l.previewGraphImport(context.Background(), 42, "neo4j", l.object.Name, r)
	if err != nil {
		t.Fatal("original complete graph preview", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestNeo4jNativeGraphImportCompleteTypedNodesDirectedRelationshipsAndExchange(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label, rel := "NyaGraphImport_"+f.Nonce, "NyaGraphEdges_"+f.Nonce
	cleanupLabelTest(t, admin, label)
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (n:$($label) {keep:42}) RETURN n", Parameters: map[string]any{"label": label}}).Rows[0][0].Node.Clone()
	l := dataLeaseTest(t, s, "label", label)
	props := graphImportPropertiesTest(t)
	b := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []Node{{ElementID: "a", Labels: []string{label, "NyaSecond_" + f.Nonce}, Properties: props}, {ElementID: "b"}, {ElementID: "reuse", Labels: []string{"must_not_replace"}, Properties: []Property{{Name: "keep", Value: Value{Kind: "integer", Text: "-1"}}}}}}
	for _, edge := range []struct{ id, start, end string }{{"forward", "a", "b"}, {"parallel", "a", "b"}, {"reverse", "b", "a"}, {"self", "a", "a"}, {"external", "a", "outside"}, {"reuse", "reuse", "a"}} {
		b.Relationships = append(b.Relationships, Relationship{ElementID: edge.id, StartElementID: edge.start, EndElementID: edge.end, Type: rel, Properties: props})
	}
	r := contract.GraphImportRequest{Bundle: b, Bindings: []contract.GraphImportBinding{{SourceID: "outside", DestinationID: seed.ElementID}, {SourceID: "reuse", DestinationID: seed.ElementID}}}
	p := graphImportPreviewTest(t, l, r)
	snapshot := p.Snapshot()
	if snapshot.Nodes != 2 || len(snapshot.Anchors) != 2 || snapshot.NativeJSON == "" {
		t.Fatal("incomplete native graph preview")
	}
	r.Bundle.Nodes[0].Properties[0].Value.Text = "later"
	copy := p.Snapshot()
	copy.Anchors[0].Node.Properties[0].Value.Text = "later"
	if !reflect.DeepEqual(snapshot, p.Snapshot()) {
		t.Fatal("preview followed later source/anchor changes")
	}
	out, err := l.submitGraphImport(context.Background(), 42, "neo4j", label, p)
	if err != nil || out == nil || !out.BeginConfirmed || !out.CommitConfirmed || out.OutcomeUnknown || out.NodesCreated != 2 || out.RelationshipsCreated != 6 || len(out.Identities) != 10 {
		t.Fatal("complete graph batch not confirmed", err, out)
	}
	var receipt graphImportReceipt
	if json.Unmarshal([]byte(out.NativeJSON), &receipt) != nil || len(receipt.Bookmarks) == 0 || receipt.RollbackAttempted {
		t.Fatal("graph commit receipt incomplete")
	}
	ids := map[string]string{}
	for _, id := range out.Identities {
		if id.Kind == "node" {
			ids[id.SourceID] = id.DestinationID
		}
	}
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id DETACH DELETE n", Parameters: map[string]any{"id": ids["b"]}})
	})
	actual := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n", Parameters: map[string]any{"id": seed.ElementID}, Mode: "read"}).Rows[0][0].Node
	if !reflect.DeepEqual(*actual, seed) {
		t.Fatal("explicit reuse replaced original anchor")
	}
	graph, err := l.exportGraph(context.Background(), l.object)
	if err != nil {
		t.Fatal("complete original database exchange", err)
	}
	if len(graph.Nodes) != 3 || len(graph.Relationships) != 6 || len(graphimport.ExternalEndpoints(graph)) != 0 {
		t.Fatal("database export omitted unlabeled nodes, foreign labels or closure", len(graph.Nodes), len(graph.Relationships))
	}
	for _, format := range []string{"json", "jsonl"} {
		var buffer bytes.Buffer
		if err = graphimport.Write(&buffer, graph, format); err != nil {
			t.Fatal(err)
		}
		copy, err := graphimport.Parse(&buffer, format)
		if err != nil || !reflect.DeepEqual(copy, graph) {
			t.Fatal("native graph file changed complete entity values", format, err)
		}
	}
	if _, err = l.submitGraphImport(context.Background(), 42, "neo4j", label, p); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("graph plan replayed", err)
	}
}

func TestNeo4jNativeGraphImportAnchorConflictAndOriginalPlanOwnership(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label := "NyaGraphConflict_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	n := nativeRunTest(t, admin, Request{Query: "CREATE (n:$($label) {keep:1}) RETURN n", Parameters: map[string]any{"label": label}}).Rows[0][0].Node
	l := dataLeaseTest(t, s, "label", label)
	r := contract.GraphImportRequest{Bundle: contract.GraphImportBundle{Format: graphimport.Format, Nodes: []Node{{ElementID: "new", Labels: []string{label}}}, Relationships: []Relationship{{ElementID: "edge", StartElementID: "new", EndElementID: "old", Type: label}}}, Bindings: []contract.GraphImportBinding{{SourceID: "old", DestinationID: n.ElementID}}}
	p := graphImportPreviewTest(t, l, r)
	foreign := dataLeaseTest(t, s, "label", label)
	if _, err := foreign.submitGraphImport(context.Background(), 42, "neo4j", label, p); err == nil {
		t.Fatal("graph plan crossed original lease")
	}
	nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id SET n.keep=2", Parameters: map[string]any{"id": n.ElementID}})
	out, err := l.submitGraphImport(context.Background(), 42, "neo4j", label, p)
	if !errors.Is(err, contract.ErrDataConflict) || out == nil || !out.RollbackConfirmed || out.CommitAttempted || out.OutcomeUnknown || len(out.Identities) != 0 {
		t.Fatal("anchor change admitted graph writes or lost rollback", err, out)
	}
	count := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", Parameters: map[string]any{"label": label}, Mode: "read"}).Rows[0][0].Text
	if count != "1" {
		t.Fatal("conflicting import created a node")
	}
	newPlan := graphImportPreviewTest(t, l, r)
	replacement := graphImportPreviewTest(t, l, r)
	if newPlan.Snapshot().NativeJSON != "" || replacement.Snapshot().NativeJSON == "" {
		t.Fatal("new graph preview did not revoke original")
	}
}

func TestNeo4jNativeGraphImportLaterChunkFailureRollsBackCompleteBatch(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label, constraint := "NyaGraphChunk_"+f.Nonce, "NyaGraphUnique_"+f.Nonce
	cleanupLabelTest(t, admin, label)
	nativeRunTest(t, admin, Request{Query: "CREATE CONSTRAINT " + graphQuote(constraint) + " FOR (n:" + graphQuote(label) + ") REQUIRE n.key IS UNIQUE"})
	t.Cleanup(func() { nativeRunTest(t, admin, Request{Query: "DROP CONSTRAINT " + graphQuote(constraint)}) })
	l := dataLeaseTest(t, s, "label", label)
	b := contract.GraphImportBundle{Format: graphimport.Format}
	for i := 0; i < graphImportChunkRows+1; i++ {
		b.Nodes = append(b.Nodes, Node{ElementID: strings.Repeat("x", i+1), Labels: []string{label}, Properties: []Property{{Name: "key", Value: Value{Kind: "string", Text: strings.Repeat("x", i%graphImportChunkRows+1)}}}})
	}
	p := graphImportPreviewTest(t, l, contract.GraphImportRequest{Bundle: b})
	out, err := l.submitGraphImport(context.Background(), 42, "neo4j", label, p)
	if err == nil || out == nil || out.CommitAttempted || out.CommitConfirmed || len(out.Identities) != 0 {
		t.Fatal("later constraint failure admitted complete graph success", err, out)
	}
	var receipt graphImportReceipt
	if json.Unmarshal([]byte(out.NativeJSON), &receipt) != nil || len(receipt.Statements) != 2 || receipt.Statements[0].Result.Summary.Counters["nodes_created"] != graphImportChunkRows || receipt.FailureCode == "" {
		t.Fatal("did not prove first native chunk before later failure")
	}
	count := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", Parameters: map[string]any{"label": label}, Mode: "read"}).Rows[0][0].Text
	if count != "0" {
		t.Fatal("earlier chunk escaped atomic rollback")
	}
}
